// Talking to Loadout on the PC.
//
// The PS5 can't type or search the network, so Loadout writes loadout.json
// into this app's folder when it installs the app: the PC's addresses and a
// device token it already paired. Everything here goes through Loadout's
// store API (/s/...) on the phone remote's port.
import {Http, FileSystem} from '@ps5-react/core';

// Loadout keeps a copy in the app's data folder current (a new PC address,
// say) without touching the app's own folder; the one Loadout installed with
// the app is the fallback.
const CONFIGS = [`${FileSystem.dataDir}/loadout.json`, `${FileSystem.appDir}/loadout.json`];

let config = null;
let base = '';

// loadConfig reads what Loadout left for the app; null if it isn't there.
export function loadConfig() {
  for (const file of CONFIGS) {
    try {
      const c = JSON.parse(FileSystem.readFile(file));
      if (c && Array.isArray(c.urls) && c.urls.length && typeof c.token === 'string') {
        config = c;
        return c;
      }
    } catch (err) {
      // missing or broken: try the next; the setup screen explains what to do
    }
  }
  return null;
}

async function call(url, options = {}) {
  const response = await Http.request(url, {
    method: options.body ? 'POST' : 'GET',
    headers: Object.assign({'X-Loadout-Token': config.token},
      options.body ? {'Content-Type': 'application/json'} : {}),
    body: options.body ? JSON.stringify(options.body) : undefined,
    maxBytes: options.maxBytes || 1024 * 1024,
    signal: options.signal,
  });
  let data = {};
  try {
    data = await response.json();
  } catch (err) {
    data = {};
  }
  if (response.status === 401) {
    const error = new Error('This PS5 is no longer paired with Loadout. Reinstall the app from Loadout on your PC.');
    error.unpaired = true;
    throw error;
  }
  if (!response.ok) throw new Error(data.error || `Loadout answered ${response.status}`);
  // actions answer 200 with an error when the PC can't do them
  if (data && typeof data.error === 'string' && data.error) throw new Error(data.error);
  return data;
}

// PS5 React runs one HTTP request at a time, so addresses are tried in turn,
// each given a few seconds: an address the PS5 can't reach (a VPN's, say)
// would otherwise hold every later request for its 30-second timeout. The
// address that worked last time goes first.
const LAST = `${FileSystem.dataDir}/loadout-last.json`;
const TRY_MS = 2500;

// timeout is an abort signal that fires after ms; the framework has no AbortController
function timeout(ms) {
  const listeners = new Set();
  const signal = {aborted: false,
    addEventListener: (type, fn) => listeners.add(fn),
    removeEventListener: (type, fn) => listeners.delete(fn)};
  const timer = setTimeout(() => { signal.aborted = true; for (const fn of [...listeners]) fn(); }, ms);
  return {signal, clear: () => clearTimeout(timer)};
}

function lastGood() {
  try {
    return JSON.parse(FileSystem.readFile(LAST)).url || '';
  } catch (err) {
    return '';
  }
}

export async function connect() {
  const last = lastGood();
  const urls = [...new Set([last, ...config.urls].filter(Boolean))];
  let error = null;
  for (const url of urls) {
    // the likely address gets longer: a connect retried once takes about 3 s
    const t = timeout(url === urls[0] ? 8000 : TRY_MS);
    try {
      const hello = await call(`${url}/s/hello`, {maxBytes: 4096, signal: t.signal});
      if (hello.app !== 'Loadout') throw new Error(`${url} isn't Loadout`);
      base = url;
      if (url !== last) {
        try { FileSystem.writeFile(LAST, JSON.stringify({url})); } catch (err) { /* only a speed-up */ }
      }
      return hello;
    } catch (err) {
      if (err.unpaired) throw err;
      error = t.signal.aborted ? new Error(`${url} didn't answer`) : err;
    } finally {
      t.clear();
    }
  }
  throw new Error(`Can't reach your PC${config.pc ? ` (${config.pc})` : ''}. Make sure Loadout is running there, even if only in the tray.` +
    (error ? `
${error.message}` : ''));
}

export const api = {
  // 4 MiB is PS5 React's most for one response: thousands of games
  games: () => call(`${base}/s/games`, {maxBytes: 4 * 1024 * 1024}),
  status: () => call(`${base}/s/status`),
  download: (key, drive) => call(`${base}/s/get`, {body: {Key: key, Drive: drive}}),
  dlcInstall: (game, file) => call(`${base}/s/dlc/install`, {body: {Game: game, File: file}}),
  saves: id => call(`${base}/s/saves?id=${encodeURIComponent(id)}`),
  saveBackup: (id, name) => call(`${base}/s/saves/backup`, {body: {ID: id, Name: name}}),
  saveRestore: backup => call(`${base}/s/saves/restore`, {body: {Backup: backup}}),
  roms: () => call(`${base}/s/roms`, {maxBytes: 4 * 1024 * 1024}),
  romGet: key => call(`${base}/s/roms/get`, {body: {Key: key}}),
  appUpdate: () => call(`${base}/s/app/update`, {body: {}}),
  // the app is about to close: Loadout swaps the new build in once it has
  appSwap: () => call(`${base}/s/app/swap`, {body: {}}),
  // the homebrew store (homebrew.page, ProsperoStore's catalog); the PC installs
  homebrew: refresh => call(`${base}/s/homebrew`, {body: {Refresh: !!refresh}, maxBytes: 2 * 1024 * 1024}),
  homebrewApp: id => call(`${base}/s/homebrew/app`, {body: {ID: id}}),
  homebrewDo: (id, action) => call(`${base}/s/homebrew/do`, {body: {ID: id, Action: action}}),
  // leftovers of stopped copies on the PS5's drives
  cleanupScan: () => call(`${base}/s/cleanup/scan`, {body: {}}),
  cleanupDelete: paths => call(`${base}/s/cleanup/delete`, {body: {Paths: paths}}),
  // one download in the queue: stop, pause, resume, up or down
  job: (id, action) => call(`${base}/s/job`, {body: {ID: id, Action: action}}),
};

// pcAddress is the address this app reached Loadout at, for the Your PS5 page
export const pcAddress = () => base.replace(/^https?:\/\//, '');

// art is a picture URL; pictures can't carry headers, so the token rides along.
// kind is cover, hero or shot0..shot5.
export function art(key, kind) {
  if (!key || !base) return null;
  return `${base}/s/art?k=${encodeURIComponent(key)}&f=${kind}&t=${encodeURIComponent(config.token)}`;
}
