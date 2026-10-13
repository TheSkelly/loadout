// Talking to Loadout on the PC.
//
// The PS5 can't type or search the network, so Loadout writes loadout.json
// into this app's folder when it installs the app: the PC's addresses and a
// device token it already paired. Everything here goes through Loadout's
// store API (/s/...) on the phone remote's port.
import {Http, FileSystem} from '@ps5-react/core';

const CONFIG = `${FileSystem.appDir}/loadout.json`;

let config = null;
let base = '';

// loadConfig reads what Loadout left for the app; null if it isn't there.
export function loadConfig() {
  try {
    const c = JSON.parse(FileSystem.readFile(CONFIG));
    if (c && Array.isArray(c.urls) && c.urls.length && typeof c.token === 'string') {
      config = c;
      return c;
    }
  } catch (err) {
    // missing or broken: the setup screen explains what to do
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
  return data;
}

// connect asks every address Loadout listed at once and uses the first that
// answers: an address the PS5 can't reach (a VPN's, say) only times out.
export function connect() {
  return new Promise((resolve, reject) => {
    let left = config.urls.length, last = null, done = false;
    for (const url of config.urls) {
      call(`${url}/s/hello`, {maxBytes: 4096}).then(hello => {
        if (done || hello.app !== 'Loadout') throw new Error(`${url} isn't Loadout`);
        done = true;
        base = url;
        resolve(hello);
      }).catch(err => {
        if (done) return;
        if (err.unpaired) { done = true; reject(err); return; }
        last = err;
        if (--left === 0) {
          reject(new Error(`Can't reach your PC${config.pc ? ` (${config.pc})` : ''}. Make sure Loadout is running there, even if only in the tray.` +
            (last ? `\n${last.message}` : '')));
        }
      });
    }
  });
}

export const api = {
  // 4 MiB is PS5 React's most for one response: thousands of games
  games: () => call(`${base}/s/games`, {maxBytes: 4 * 1024 * 1024}),
  status: () => call(`${base}/s/status`),
  download: (key, drive) => call(`${base}/s/get`, {body: {Key: key, Drive: drive}}),
};

// art is a picture URL; pictures can't carry headers, so the token rides along.
export function art(key, kind) {
  if (!key || !base) return null;
  return `${base}/s/art?k=${encodeURIComponent(key)}&f=${kind}&t=${encodeURIComponent(config.token)}`;
}
