// Loadout for PS5: browse the games in Loadout on your PC and download them
// to this console. The PC does the work (it builds the image if needed and
// sends it); this app picks the game and shows the progress. It also installs
// DLC packages, backs up and restores saves, and downloads games from the
// PC's ROM library to the emulators on this PS5.
//
// Built with PS5 React (GPL-3.0-or-later); see NOTICE in the app folder.
import {memo, useCallback, useEffect, useMemo, useRef, useState} from 'react';
import {AppRegistry, View, Text, Image, ScrollView, VirtualList, FocusScope, useIsFocused, useController, FileSystem, BackHandler} from '@ps5-react/core';
import {loadConfig, connect, api, art, pcAddress} from './api.js';
import logo from './assets/logo.png';

const T = 'font-inter text-text';
const POLL_BUSY = 1500, POLL_IDLE = 5000, GAMES_EVERY = 60000;
// the backdrop follows focus once it settles, so a held key doesn't load every hero on the way
const BACKDROP_WAIT = 350;

// on the PS5 a send is a download
const WORD = {queued: 'Queued', building: 'Preparing on PC', waiting: 'Waiting to download', sending: 'Downloading', sent: 'Downloaded', failed: 'Download failed'};
const busy = p => !!p && !!p.state && p.state !== 'sent' && p.state !== 'failed';

const size = b => !(b > 0) ? '' : b >= 1e12 ? `${(b / 1e12).toFixed(2)} TB` : b >= 1e9 ? `${(b / 1e9).toFixed(1)} GB` : b >= 1e6 ? `${Math.round(b / 1e6)} MB` : `${Math.max(1, Math.round(b / 1e3))} KB`;
const speed = b => b > 0 ? `${Math.round(b / 1e6)} MB/s` : '';
const dur = s => !(s > 0) ? '' : s >= 3600 ? `${Math.floor(s / 3600)} h ${Math.round(s % 3600 / 60)} min` : s >= 60 ? `${Math.round(s / 60)} min` : `${Math.round(s)} s`;
const MONTHS = ['jan', 'feb', 'mar', 'apr', 'may', 'jun', 'jul', 'aug', 'sep', 'oct', 'nov', 'dec'];
const MON = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
// firstSeen comes as "Oct 1, 2026"; a number sorts it
const seen = s => {
  const m = /^([A-Za-z]{3})[a-z]* (\d+), (\d{4})/.exec(s || '');
  return m ? Number(m[3]) * 10000 + (MONTHS.indexOf(m[1].toLowerCase()) + 1) * 100 + Number(m[2]) : 0;
};
const when = iso => {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return '';
  const h = d.getHours(), m = d.getMinutes();
  return `${MON[d.getMonth()]} ${d.getDate()}, ${d.getFullYear()}  ${h % 12 || 12}:${m < 10 ? '0' : ''}${m} ${h < 12 ? 'AM' : 'PM'}`;
};
const byName = (a, b) => a.name.toLowerCase() < b.name.toLowerCase() ? -1 : 1;
const sleep = ms => new Promise(done => setTimeout(done, ms));
// the font has Latin letters only: a description in Japanese, say, would show as ????
const readable = t => !!t && !/[\u0370-\u1fff\u3000-\uffff]/.test(t);

// Each build of the app carries a stamp file (Loadout writes it). The PS5
// resumes a suspended app instead of starting it again, so an update on disk
// can sit unused: the app compares the build it started as with the one on
// the PC and offers to update and restart.
const STAMP = `${FileSystem.appDir}/zz-loadout-build.txt`;
const readStamp = () => {
  try {
    return String(FileSystem.readFile(STAMP)).trim();
  } catch (err) {
    return '';
  }
};
const LAUNCHED = readStamp();

// ---------- small pieces ----------

// class names are compiled at build time, so colors picked at run time go in style
const C = {accent: '#4B9CD3', royal: '#2F5BD8', mint: '#5BC8A0', coral: '#EF6F6C', sky: '#7CC4F5', muted: '#8FA3C4'};

// tiles are square: PS5 icons are 512 x 512
const TILE = 176, NAME_H = 21, TILE_H = TILE + 8 + NAME_H;

// width is in logical pixels: the bar's fill is sized from it
function Bar({pct, width, color = C.accent}) {
  const fill = Math.round(width * Math.max(0, Math.min(100, pct)) / 100);
  return (
    <View className="h-[6px] rounded-full bg-line overflow-hidden" style={{width}}>
      <View className="h-[6px]" style={{width: fill, backgroundColor: color}} />
    </View>
  );
}

// what a game is doing, for its badge and the spotlight
function stateOf(game, prog) {
  if (busy(prog)) return {label: WORD[prog.state] || prog.state, color: C.accent};
  if (prog && prog.state === 'failed') return {label: 'Download failed', color: C.coral};
  if (game.onPs5) return {label: 'On your PS5', color: C.sky};
  if (game.ready) return {label: 'Ready to download', color: C.mint};
  if (game.canGet) return {label: 'Your PC prepares it first', color: C.muted};
  return null;
}

// what the game is doing, top right of its tile
function Badge({game, prog}) {
  let label = '', color = C.mint;
  if (busy(prog)) { label = WORD[prog.state] || prog.state; color = C.accent; }
  else if (prog && prog.state === 'failed') { label = 'Failed'; color = C.coral; }
  else if (game.onPs5) { label = 'Installed'; color = C.sky; }
  if (!label) return null;
  return (
    <View className="absolute right-[10px] top-[10px] px-[10px] py-[3px] rounded-lg bg-black/75">
      <Text className="font-inter text-xs" style={{color}}>{label}</Text>
    </View>
  );
}

// the cover and name read the tile's focus to draw its highlight
function TileArt({game, prog}) {
  const focused = useIsFocused();
  const cover = art(game.cover, 'cover');
  return (
    <View focused={focused} className="w-[176px] h-[176px] rounded-2xl bg-raised border-[3px] border-transparent overflow-hidden
      transition focused:border-accent focused:scale-105">
      {cover
        ? <Image source={{uri: cover}} resizeMode="cover" className="w-[170px] h-[170px] rounded-xl bg-raised" />
        : <View className="w-[170px] h-[170px] items-center justify-center p-[14px]"><Text className={`${T} text-base text-center line-clamp-4`}>{game.name}</Text></View>}
      {game.platform ? (
        <View className="absolute left-[10px] top-[10px] px-[8px] py-[2px] rounded-md bg-black/75">
          <Text className="font-inter text-xs text-text">{game.platform}</Text>
        </View>
      ) : null}
      <Badge game={game} prog={prog} />
      {busy(prog) && prog.pct > 0 ? <View className="absolute left-[10px] bottom-[10px]"><Bar pct={prog.pct / 10} width={150} /></View> : null}
    </View>
  );
}

function TileName({name}) {
  const focused = useIsFocused();
  return <Text focused={focused} className="font-inter text-sm text-muted focused:text-text focused:font-bold truncate w-[176px]">{name}</Text>;
}

// `end` is set on a rail's last tile: Right stops there instead of dropping to the next rail
const Tile = memo(function Tile({game, prog, onOpen, onSpot, end}) {
  return (
    <View onPress={() => onOpen(game.key)} onFocus={() => onSpot(game.key)} focusKey={end} nextFocusRight={end} className="w-[176px] gap-[8px]">
      <TileArt game={game} prog={prog} />
      <TileName name={game.name} />
    </View>
  );
});

function Heading({title, note, pad = 64}) {
  return (
    <View className="flex-row items-end gap-[12px]" style={{paddingLeft: pad, paddingRight: pad}}>
      <Text className={`${T} text-lg font-bold`}>{title}</Text>
      {note ? <Text className="font-inter text-sm text-faint">{note}</Text> : null}
    </View>
  );
}

// a rail of tiles; the ScrollView itself is the row, so the row overflows it and scrolls
const Shelf = memo(function Shelf({id, title, note, games, progress, onOpen, onSpot}) {
  if (!games.length) return null;
  const last = games.length - 1;
  return (
    <View scrollAnchor className="gap-[4px] pt-[8px] pb-[40px]">
      <Heading title={title} note={note} />
      <ScrollView horizontal className="w-screen h-[235px] flex-row items-start gap-[22px] py-[12px]">
        <View className="w-[42px] h-[1px]" />
        {games.map((g, i) => <Tile key={g.key} game={g} prog={progress[g.key]} onOpen={onOpen} onSpot={onSpot} end={i === last ? `end:${id}` : undefined} />)}
        <View className="w-[42px] h-[1px]" />
      </ScrollView>
    </View>
  );
});

function Activity({status}) {
  const lane = !status ? null : status.ps5.busy ? status.ps5 : status.build.busy ? status.build : null;
  if (!lane) return null;
  const sub = [lane.total > 1 ? `${lane.done} of ${lane.total}` : '', speed(lane.speed), lane.eta ? `${dur(lane.eta)} left` : ''].filter(Boolean).join(' · ');
  return (
    <View className="flex-row items-center gap-[12px] px-[16px] py-[7px] rounded-full bg-surface/90 border border-line">
      <View className="gap-[1px]">
        <Text className="font-inter text-xs text-text truncate w-[240px]">{lane.what || 'Working…'}</Text>
        {sub ? <Text className="font-inter text-xs text-muted truncate w-[240px]">{sub}</Text> : null}
      </View>
      <Bar pct={lane.pct / 10} width={100} color={status.ps5.busy ? C.sky : C.accent} />
    </View>
  );
}

function Logo({big}) {
  return big ? (
    <View className="items-center gap-[10px]">
      <Image source={logo} resizeMode="contain" className="w-[128px] h-[128px]" />
      <Text className="font-inter text-[30px] leading-[36px] font-bold tracking-widest text-accent">LOADOUT</Text>
    </View>
  ) : (
    <View className="flex-row items-center gap-[10px]">
      <Image source={logo} resizeMode="contain" className="w-[40px] h-[40px]" />
      <Text className="font-inter text-xl font-bold tracking-wider text-accent">LOADOUT</Text>
    </View>
  );
}

// Orbit-style: a white pill for the main action, grey for the rest, a
// Carolina blue ring on whatever has focus
function Button({label, onPress, primary, disabled, small}) {
  if (disabled) {
    return (
      <View focusable className={small ? 'px-[20px] py-[7px] rounded-full bg-raised border-[3px] border-transparent focused:border-accent'
        : 'px-[34px] py-[12px] rounded-full bg-raised border-[3px] border-transparent focused:border-accent'}>
        <Text className={small ? 'font-inter text-sm text-faint' : 'font-inter text-base text-faint'}>{label}</Text>
      </View>
    );
  }
  if (small) {
    return primary ? (
      <View onPress={onPress} className="px-[20px] py-[7px] rounded-full bg-text border-[3px] border-transparent focused:border-accent">
        <Text className="font-inter text-sm font-bold text-ink">{label}</Text>
      </View>
    ) : (
      <View onPress={onPress} className="px-[20px] py-[7px] rounded-full bg-raised border-[3px] border-transparent focused:border-accent">
        <Text className="font-inter text-sm text-text">{label}</Text>
      </View>
    );
  }
  return primary ? (
    <View onPress={onPress} className="px-[44px] py-[12px] rounded-full bg-text border-[3px] border-transparent transition focused:border-accent focused:scale-105">
      <Text className="font-inter text-base font-bold text-ink">{label}</Text>
    </View>
  ) : (
    <View onPress={onPress} className="px-[34px] py-[12px] rounded-full bg-raised/90 border-[3px] border-transparent transition focused:border-accent focused:scale-105">
      <Text className="font-inter text-base text-text">{label}</Text>
    </View>
  );
}

// a round button, like Orbit's heart
function RoundButton({label, on, onPress}) {
  return on ? (
    <View onPress={onPress} className="w-[52px] h-[52px] items-center justify-center rounded-full bg-accent border-[3px] border-transparent transition focused:border-text focused:scale-105">
      <Text className="font-inter text-lg font-bold text-ink">{label}</Text>
    </View>
  ) : (
    <View onPress={onPress} className="w-[52px] h-[52px] items-center justify-center rounded-full bg-black/40 border-2 border-muted transition focused:border-accent focused:scale-105">
      <Text className="font-inter text-lg text-text">{label}</Text>
    </View>
  );
}

// what the controller does here, bottom right like the PS5's own menus
function Hint({btn, label}) {
  return (
    <View className="flex-row items-center gap-[8px]">
      {btn === 'cross' || btn === 'circle' ? (
        <View className="w-[24px] h-[24px] items-center justify-center rounded-full border-2 border-text">
          {btn === 'cross' ? <Text className="font-inter text-sm text-text">×</Text> : <View className="w-[10px] h-[10px] rounded-full border-2 border-text" />}
        </View>
      ) : (
        <View className="flex-row gap-[4px]">
          {btn.split(' ').map(b => (
            <View key={b} className="px-[6px] h-[24px] items-center justify-center rounded-md border-2 border-text">
              <Text className="font-inter text-xs text-text">{b}</Text>
            </View>
          ))}
        </View>
      )}
      <Text className="font-inter text-sm text-text">{label}</Text>
    </View>
  );
}

function Hints({items}) {
  return (
    <View className="absolute right-[48px] bottom-[20px] flex-row items-center gap-[26px] px-[16px] py-[8px] rounded-full bg-black/60">
      {items.map(it => <Hint key={it.label} btn={it.btn} label={it.label} />)}
    </View>
  );
}

function Chip({children}) {
  return (
    <View className="px-[12px] py-[4px] rounded-full bg-surface/90 border border-line">
      <Text className="font-inter text-xs text-text">{children}</Text>
    </View>
  );
}

// a question over the screen; Circle cancels
function Confirm({title, body, yes, onYes, onClose}) {
  return (
    <FocusScope autoFocus trap onBack={() => { onClose(); return true; }}>
      <View className="absolute inset-0 w-screen h-screen bg-black/70 items-center justify-center">
        <View className="w-[620px] p-[32px] gap-[16px] rounded-3xl bg-surface border-2 border-royal">
          <Text className={`${T} text-xl font-bold`}>{title}</Text>
          {body ? <Text className="font-inter text-base text-muted">{body}</Text> : null}
          <View className="flex-row gap-[14px] pt-[8px]">
            <Button primary label={yes} onPress={() => { onClose(); onYes(); }} />
            <Button label="Cancel" onPress={onClose} />
          </View>
        </View>
      </View>
    </FocusScope>
  );
}

// ---------- screens ----------

function Message({title, body, action, onAction}) {
  return (
    <FocusScope autoFocus>
      <View className="w-screen h-screen bg-ink items-center justify-center gap-[16px] px-[160px]">
        <View className="absolute inset-0 bg-gradient-to-b from-ink2 via-ink to-black" />
        <Logo big />
        <Text className={`${T} text-title font-bold pt-[18px]`}>{title}</Text>
        <Text className="font-inter text-base text-muted text-center">{body}</Text>
        {action ? <View className="pt-[12px]"><Button primary label={action} onPress={onAction} /></View> : null}
      </View>
    </FocusScope>
  );
}

// behind Discover: the focused game's art, full screen, darkened toward the
// text on the left and the rails at the bottom
function Backdrop({hero}) {
  const [shown, setShown] = useState(hero);
  useEffect(() => {
    const t = setTimeout(() => setShown(hero), BACKDROP_WAIT);
    return () => clearTimeout(t);
  }, [hero]);
  const uri = art(shown, 'hero');
  return (
    <View className="absolute left-0 top-0 w-screen h-screen overflow-hidden">
      <View className="absolute inset-0 bg-gradient-to-br from-ink2 via-ink to-black" />
      {uri ? <Image key={uri} source={{uri}} resizeMode="cover" className="absolute right-0 top-0 w-[1100px] h-[620px]" /> : null}
      <View className="absolute left-0 top-0 w-[860px] h-screen bg-gradient-to-r from-ink via-ink/80 to-transparent" />
      <View className="absolute left-0 top-0 w-screen h-[110px] bg-gradient-to-b from-ink/80 to-transparent" />
      <View className="absolute left-0 bottom-0 w-screen h-[420px] bg-gradient-to-t from-ink via-ink/90 to-transparent" />
    </View>
  );
}

// the big title over the art: what the game is, View game and a favourite star
function Hero({game, prog, onOpen}) {
  const favs = useFavs();
  if (!game) return <View className="h-[300px]" />;
  const st = stateOf(game, prog);
  const meta = [game.platform, size(game.size), (game.genres || []).slice(0, 2).join(', '), game.rating ? `★ ${game.rating.toFixed(1)}` : '',
    game.dlc && game.dlc.length ? `${game.dlc.length} DLC${dlcDone(game) ? ` (${dlcDone(game) === game.dlc.length ? 'all' : dlcDone(game)} installed)` : ''}` : ''].filter(Boolean).join('   ·   ');
  const fav = favs.has(game.key);
  return (
    <View className="h-[300px] w-[640px] px-[64px] pt-[18px] gap-[10px]">
      <Text className="font-inter text-xs tracking-[4px]" style={{color: st ? st.color : C.muted}}>{(st ? st.label : 'Discover').toUpperCase()}</Text>
      <Text className={`${T} text-[44px] leading-[50px] font-bold line-clamp-2`}>{game.name}</Text>
      <Text className="font-inter text-sm text-muted line-clamp-1">{meta}</Text>
      {readable(game.desc) ? <Text className="font-inter text-sm text-text/80 line-clamp-2">{game.desc}</Text> : <View className="h-[42px]" />}
      <FocusScope>
        <View className="flex-row items-center gap-[16px] pt-[6px]">
          <Button primary label="View game" onPress={() => onOpen(game.key)} />
          <RoundButton label="★" on={fav} onPress={() => toggleFav(game.key)} />
        </View>
      </FocusScope>
    </View>
  );
}

// Orbit's tabs: plain words, the open one on a grey pill
function Tab({label, on, onPress}) {
  return on ? (
    <View onPress={onPress} className="px-[18px] py-[7px] rounded-full bg-raised border-[3px] border-transparent focused:border-accent">
      <Text className="font-inter text-base text-text">{label}</Text>
    </View>
  ) : (
    <View onPress={onPress} className="px-[18px] py-[7px] rounded-full border-[3px] border-transparent focused:border-accent">
      <Text className="font-inter text-base text-muted">{label}</Text>
    </View>
  );
}

const TAB_NAMES = {games: 'Discover', browse: 'Browse', library: 'Library', brew: 'Homebrew', emu: 'Emulators', dl: 'Downloads', console: 'Your PS5'};

function TopBar({tab, setTab, status, update}) {
  const n = status && status.queue ? status.queue.length : 0;
  return (
    <View className="flex-row items-center px-[48px] pt-[18px] gap-[16px]">
      <Logo />
      <View className="flex-1" />
      <FocusScope>
        <View className="flex-row gap-[4px]">
          {TABS.map(t => <Tab key={t} label={t === 'dl' && n ? `Downloads · ${n}` : TAB_NAMES[t]} on={tab === t} onPress={() => setTab(t)} />)}
        </View>
      </FocusScope>
      <View className="flex-1" />
      {update ? (
        <View className="flex-row items-center gap-[8px] px-[14px] py-[6px] rounded-full bg-accent">
          <Text className="font-inter text-sm font-bold text-ink">Update ready</Text>
          <Text className="font-inter text-xs text-ink">Triangle</Text>
        </View>
      ) : <Activity status={status} />}
    </View>
  );
}

// ---------- finding a game: search, filters, sort, favourites ----------

// favourites live on this PS5, in the app's data folder
const FAVS = `${FileSystem.dataDir}/loadout-favs.json`;
const favStore = {keys: new Set(), listeners: new Set()};
try {
  const saved = JSON.parse(FileSystem.readFile(FAVS));
  if (Array.isArray(saved)) favStore.keys = new Set(saved);
} catch (err) {
  // none yet
}
function toggleFav(key) {
  const keys = new Set(favStore.keys);
  if (keys.has(key)) keys.delete(key); else keys.add(key);
  favStore.keys = keys;
  try { FileSystem.writeFile(FAVS, JSON.stringify([...keys])); } catch (err) { /* kept until the app closes */ }
  for (const fn of favStore.listeners) fn(keys);
}
function useFavs() {
  const [keys, setKeys] = useState(favStore.keys);
  useEffect(() => {
    favStore.listeners.add(setKeys);
    return () => favStore.listeners.delete(setKeys);
  }, []);
  return keys;
}

const GB = 1e9;
// each filter is a list of choices; pressing it moves to the next
const FILTERS = {
  platform: [{label: 'All platforms'}, {label: 'PS5', test: g => g.platform === 'PS5'}, {label: 'PS4', test: g => g.platform === 'PS4'}],
  format: [{label: 'Any format'}, {label: 'Image ready', test: g => g.ready}, {label: 'Folder only', test: g => !g.ready}],
  size: [{label: 'Any size'}, {label: 'Under 20 GB', test: g => g.size < 20 * GB}, {label: '20 to 50 GB', test: g => g.size >= 20 * GB && g.size < 50 * GB},
    {label: 'Over 50 GB', test: g => g.size >= 50 * GB}],
  where: [{label: 'Anywhere'}, {label: 'Not on PS5', test: g => !g.onPs5}, {label: 'On PS5', test: g => !!g.onPs5}],
};
const SORTS = [
  {label: 'A to Z', by: byName},
  {label: 'Newest', by: (a, b) => seen(b.firstSeen) - seen(a.firstSeen) || byName(a, b)},
  {label: 'Largest', by: (a, b) => (b.size || 0) - (a.size || 0) || byName(a, b)},
  {label: 'Smallest', by: (a, b) => (a.size || 0) - (b.size || 0) || byName(a, b)},
  {label: 'Top rated', by: (a, b) => (b.rating || 0) - (a.rating || 0) || byName(a, b)},
];
const NO_FIND = {text: '', platform: 0, format: 0, size: 0, where: 0, sort: 0, favs: false};
const finding = f => !!f.text || f.platform || f.format || f.size || f.where || f.favs;

// words match anywhere in the name or title ID, in any order
function findGames(games, f, favs) {
  const words = f.text.toLowerCase().split(' ').filter(Boolean);
  const tests = Object.keys(FILTERS).map(k => FILTERS[k][f[k]].test).filter(Boolean);
  return games.filter(g => {
    if (f.favs && !favs.has(g.key)) return false;
    if (tests.some(t => !t(g))) return false;
    const hay = `${g.name} ${g.id || ''}`.toLowerCase();
    return words.every(w => hay.includes(w));
  }).sort(SORTS[f.sort].by);
}

function Pill({label, on, onPress}) {
  return on ? (
    <View onPress={onPress} className="px-[16px] py-[7px] rounded-full bg-accent border-[3px] border-transparent focused:border-text">
      <Text className="font-inter text-sm font-bold text-ink">{label}</Text>
    </View>
  ) : (
    <View onPress={onPress} className="px-[16px] py-[7px] rounded-full bg-surface/90 border-[3px] border-transparent focused:border-accent">
      <Text className="font-inter text-sm text-text">{label}</Text>
    </View>
  );
}

function FindBar({find, setFind, onSearch}) {
  const next = k => setFind(f => Object.assign({}, f, {[k]: (f[k] + 1) % FILTERS[k].length}));
  return (
    <View scrollAnchor className="flex-row flex-wrap items-center gap-[10px] px-[64px] pt-[14px]">
      <Pill label={find.text ? `Search: ${find.text}` : 'Search'} on={!!find.text} onPress={onSearch} />
      {Object.keys(FILTERS).map(k => <Pill key={k} label={FILTERS[k][find[k]].label} on={find[k] > 0} onPress={() => next(k)} />)}
      <Pill label={find.favs ? '★ Favourites' : 'Favourites'} on={find.favs} onPress={() => setFind(f => Object.assign({}, f, {favs: !f.favs}))} />
      <Pill label={`Sort: ${SORTS[find.sort].label}`} on={find.sort > 0} onPress={() => setFind(f => Object.assign({}, f, {sort: (f.sort + 1) % SORTS.length}))} />
      {finding(find) || find.sort ? <Pill label="Clear" onPress={() => setFind(NO_FIND)} /> : null}
    </View>
  );
}

// the PS5 React keyboard isn't in console builds: letters on a grid instead
const KEYS = ['ABCDEFGHIJ', 'KLMNOPQRST', 'UVWXYZ0123', '456789:-\'&'];
function Key({label, wide, onPress}) {
  return wide ? (
    <View onPress={onPress} className="w-[178px] h-[56px] items-center justify-center rounded-xl bg-raised border-[3px] border-transparent focused:border-accent focused:bg-royal">
      <Text className="font-inter text-base text-text">{label}</Text>
    </View>
  ) : (
    <View onPress={onPress} className="w-[56px] h-[56px] items-center justify-center rounded-xl bg-raised border-[3px] border-transparent focused:border-accent focused:bg-royal">
      <Text className="font-inter text-lg font-bold text-text">{label}</Text>
    </View>
  );
}

function Keyboard({text, count, onText, onClose}) {
  const type = c => onText((text + c).slice(0, 40));
  const keys = useCallback(action => {
    if (action === 'square') { onText(text.slice(0, -1)); return true; }
    if (action === 'triangle') { onText((text + ' ').slice(0, 40)); return true; }
    return false;
  }, [text, onText]);
  return (
    <FocusScope autoFocus trap onAction={keys} onBack={() => { onClose(); return true; }}>
      <View className="absolute inset-0 w-screen h-screen bg-black/75 items-center justify-center">
        <View className="p-[30px] gap-[14px] rounded-3xl bg-surface border-2 border-royal">
          <Text className="font-inter text-xs text-faint tracking-widest">SEARCH YOUR GAMES</Text>
          <View className="w-[638px] h-[58px] px-[18px] justify-center rounded-xl bg-ink border-2 border-line">
            <Text className={`${T} text-xl`}>{text ? `${text}_` : '_'}</Text>
          </View>
          <Text className="font-inter text-sm text-muted">{text ? `${count} game${count === 1 ? '' : 's'} match` : 'Type a few letters of the name'}</Text>
          {KEYS.map(row => (
            <View key={row} className="flex-row gap-[8px]">
              {row.split('').map(c => <Key key={c} label={c} onPress={() => type(c)} />)}
            </View>
          ))}
          <View className="flex-row gap-[8px]">
            <Key wide label="Space" onPress={() => type(' ')} />
            <Key wide label="Delete" onPress={() => onText(text.slice(0, -1))} />
            <Key wide label="Clear" onPress={() => onText('')} />
          </View>
          <View className="flex-row items-center gap-[14px] pt-[4px]">
            <Button primary label="Done" onPress={onClose} />
            <Text className="font-inter text-xs text-faint">Square deletes · Triangle adds a space · Circle closes</Text>
          </View>
        </View>
      </View>
    </FocusScope>
  );
}

// the search and filters outlast leaving the tab or opening a game
let lastFind = NO_FIND;

// Discover: the focused game big over its art, rails of games below
function Games({games, progress, onOpen}) {
  const [spot, setSpot] = useState(null);
  const favs = useFavs();
  const shelves = useMemo(() => {
    const moving = games.filter(g => busy(progress[g.key]));
    const recent = games.filter(g => seen(g.firstSeen)).sort((a, b) => seen(b.firstSeen) - seen(a.firstSeen) || byName(a, b)).slice(0, 15);
    const notOn = games.filter(g => !g.onPs5 && g.canGet);
    const dlc = games.filter(g => g.dlc && g.dlc.some(d => d.pkg && !d.installed));
    const rated = games.filter(g => g.rating >= 4).sort((a, b) => b.rating - a.rating || byName(a, b)).slice(0, 15);
    // the genres with the most games get rails of their own
    const count = {};
    for (const g of games) for (const x of g.genres || []) count[x] = (count[x] || 0) + 1;
    const genres = Object.keys(count).filter(x => count[x] >= 4).sort((a, b) => count[b] - count[a]).slice(0, 3)
      .map(x => ({name: x, games: games.filter(g => (g.genres || []).includes(x))}));
    const fav = games.filter(g => favs.has(g.key));
    return {moving, recent, notOn, dlc, rated, genres, fav};
  }, [games, progress, favs]);
  const onSpot = useCallback(key => { setSpot(key); tellSpot(key); }, []);
  const game = games.find(g => g.key === spot) || shelves.recent[0] || games[0];
  const rail = (id, title, list, note) => <Shelf id={id} title={title} note={note} games={list} progress={progress} onOpen={onOpen} onSpot={onSpot} />;
  return (
    <View className="flex-1">
      <Hero game={game} prog={game && progress[game.key]} onOpen={onOpen} />
      <View className="flex-1 overflow-hidden">
      <FocusScope autoFocus>
        <ScrollView className="flex-1">
          {rail('moving', 'Downloading', shelves.moving)}
          {rail('recent', 'New in your library', shelves.recent)}
          {rail('fav', 'Favourites', shelves.fav)}
          {rail('noton', 'Ready for your PS5', shelves.notOn, `${shelves.notOn.length} games`)}
          {rail('rated', 'Top rated', shelves.rated)}
          {rail('dlc', 'DLC to install', shelves.dlc, 'open a game to install its DLC')}
          {shelves.genres.map(x => <Shelf key={x.name} id={`g:${x.name}`} title={x.name} games={x.games} progress={progress} onOpen={onOpen} onSpot={onSpot} />)}
          <View className="h-[70px]" />
          {!games.length ? <Text className="font-inter text-base text-muted px-[64px] pt-[30px]">No games in Loadout yet. Pick your games folder in Loadout on the PC.</Text> : null}
        </ScrollView>
      </FocusScope>
      </View>
    </View>
  );
}

// a page with a big title, like Orbit's Browse and Library
function PageTitle({kicker, title, note}) {
  return (
    <View className="px-[64px] pt-[22px] gap-[6px]">
      {kicker ? <Text className="font-inter text-xs text-muted tracking-[4px]">{kicker}</Text> : null}
      <Text className={`${T} text-[40px] leading-[48px] font-bold`}>{title}</Text>
      {note ? <Text className="font-inter text-sm text-muted">{note}</Text> : null}
    </View>
  );
}

// a grid of tiles in a page that scrolls
function GameGrid({list, progress, onOpen}) {
  const renderItem = useCallback(({item}) => <Tile game={item} prog={progress[item.key]} onOpen={onOpen} onSpot={tellSpot} />, [progress, onOpen]);
  return (
    <View className="px-[64px] pt-[10px]">
      <VirtualList data={list} keyExtractor={g => g.key} renderItem={renderItem}
        numColumns={6} itemHeight={TILE_H} rowGap={26} columnGap={18} />
    </View>
  );
}

// Browse: every game, searched, filtered and sorted
function Browse({games, progress, onOpen}) {
  const [find, setFindState] = useState(lastFind);
  const [typing, setTyping] = useState(false);
  const setFind = useCallback(f => setFindState(old => (lastFind = typeof f === 'function' ? f(old) : f)), []);
  const favs = useFavs();
  const list = useMemo(() => findGames(games, find, favs), [games, find, favs]);
  return (
    <View className="flex-1">
      <PageTitle kicker="BROWSE" title={find.text ? `Results for ${find.text}` : 'All games'}
        note={`${list.length} of ${games.length} games · ${SORTS[find.sort].label}`} />
      <FocusScope autoFocus>
        <ScrollView className="flex-1">
          <FindBar find={find} setFind={setFind} onSearch={() => setTyping(true)} />
          <View scrollAnchor className="pt-[14px] pb-[70px]">
            {list.length ? <GameGrid list={list} progress={progress} onOpen={onOpen} />
              : <Text className="font-inter text-base text-muted px-[64px] pt-[10px]">Nothing matches. Change the search or press Clear.</Text>}
          </View>
        </ScrollView>
      </FocusScope>
      {typing ? <Keyboard text={find.text} count={list.length}
        onText={text => setFind(f => Object.assign({}, f, {text}))} onClose={() => setTyping(false)} /> : null}
    </View>
  );
}

// Library: what's on this PS5
function Library({games, progress, onOpen}) {
  const on = useMemo(() => games.filter(g => g.onPs5), [games]);
  const drives = {};
  for (const g of on) {
    const d = (g.onPs5 || '').split(' ')[0] || 'PS5';
    drives[d] = (drives[d] || 0) + 1;
  }
  return (
    <View className="flex-1">
      <PageTitle kicker="LIBRARY" title="On your PS5"
        note={on.length ? `${on.length} games, ${size(on.reduce((n, g) => n + (g.size || 0), 0))}` : 'Games you download to this PS5 show here.'} />
      <FocusScope autoFocus>
        <ScrollView className="flex-1">
          <View scrollAnchor className="pt-[16px] pb-[70px]">
            {on.length ? <GameGrid list={on} progress={progress} onOpen={onOpen} />
              : <View focusable className="mx-[64px] w-[1px] h-[1px]" />}
          </View>
        </ScrollView>
      </FocusScope>
    </View>
  );
}

// Your PS5: the connection, where downloads go, the drives and what's moving
function Card({title, right, children}) {
  return (
    <View className="w-[560px] p-[22px] gap-[14px] rounded-3xl bg-surface/90 border border-line">
      <View className="flex-row items-center">
        <Text className={`${T} text-lg font-bold`}>{title}</Text>
        <View className="flex-1" />
        {right ? <Text className="font-inter text-sm text-muted">{right}</Text> : null}
      </View>
      {children}
    </View>
  );
}

function Console({status, drive, setDrive, setTab, onChanged}) {
  const [asking, setAsking] = useState(null);
  const drives = (status && status.drives) || [];
  const queue = (status && status.queue) || [];
  const now = queue.filter(q => q.state !== 'queued' && q.state !== 'paused');
  const lane = status && status.ps5;
  return (
    <View className="flex-1">
      <View className="flex-row items-end pr-[64px]">
        <PageTitle kicker="CONSOLE" title="Your PS5" />
        <View className="flex-1" />
        <View className="flex-row items-center gap-[8px] px-[16px] py-[7px] rounded-full bg-surface border border-line">
          <View className="w-[9px] h-[9px] rounded-full" style={{backgroundColor: status ? C.mint : C.coral}} />
          <Text className="font-inter text-sm text-text">{status ? 'Connected' : 'Not connected'}</Text>
        </View>
      </View>
      <FocusScope autoFocus>
        <ScrollView className="flex-1">
          <View className="flex-row flex-wrap gap-[20px] px-[64px] pt-[20px] pb-[70px]">
            <Card title="Connection" right={status ? `Loadout ${status.version}` : ''}>
              <Text className={`${T} text-xl font-bold`}>{(status && status.pc) || 'Your PC'}</Text>
              <Text className="font-inter text-sm text-muted">{pcAddress()}</Text>
              <Text className="font-inter text-sm text-muted">{status && status.ps5Online ? 'Your PC can reach this PS5. Ready for downloads.' : 'Your PC can\'t reach this PS5 right now. Check its FTP server.'}</Text>
              {LAUNCHED ? <Text className="font-inter text-xs text-faint">{`App build ${LAUNCHED.slice(0, 7)}`}</Text> : null}
              <Button small label="Refresh" onPress={onChanged} />
            </Card>
            <Card title="Downloads go to" right="for new downloads">
              {drives.length ? drives.map(d => (
                <View key={d.root} onPress={() => setDrive(d.root)} selected={d.root === drive}
                  className="flex-row items-center gap-[12px] px-[16px] py-[10px] rounded-2xl bg-raised/70 border-[3px] border-transparent selected:border-royal focused:border-accent">
                  <View className="w-[18px] h-[18px] rounded-full border-2 border-text items-center justify-center">
                    {d.root === drive ? <View className="w-[8px] h-[8px] rounded-full bg-text" /> : null}
                  </View>
                  <View className="flex-1">
                    <Text className={`${T} text-base`}>{d.label || d.short}</Text>
                    <Text className="font-inter text-xs text-muted">{d.free >= 0 ? `${size(d.free)} free` : 'free space unknown'}</Text>
                  </View>
                </View>
              )) : <Text className="font-inter text-sm text-muted">Loadout hasn't read this PS5's drives yet.</Text>}
            </Card>
            <Card title="Storage" right={drives.length ? `${drives.length} drive${drives.length === 1 ? '' : 's'}` : ''}>
              {drives.map(d => (
                <View key={d.root} focusable className="gap-[8px] py-[4px] rounded-xl border-[3px] border-transparent focused:border-accent px-[6px]">
                  <View className="flex-row">
                    <Text className={`${T} text-base`}>{d.label || d.short}</Text>
                    <View className="flex-1" />
                    <Text className="font-inter text-sm text-text">{d.free >= 0 ? `${size(d.free)} free` : ''}</Text>
                  </View>
                  <Bar pct={d.total > 0 && d.free >= 0 ? (d.total - d.free) * 100 / d.total : 0} width={490} color={C.accent} />
                  <Text className="font-inter text-xs text-muted">{d.total > 0 ? `${size(d.total)} total` : ''}</Text>
                </View>
              ))}
            </Card>
            <Card title="Active transfers" right={lane && lane.speed ? speed(lane.speed) : ''}>
              {now.length ? now.map(q => (
                <View key={q.id} className="gap-[8px]">
                  <View className="flex-row">
                    <Text className={`${T} text-base truncate w-[340px]`}>{q.name}</Text>
                    <View className="flex-1" />
                    <Text className="font-inter text-sm text-muted">{QSTATE[q.state] || q.state}</Text>
                  </View>
                  <Bar pct={q.pct / 10} width={510} color={C.sky} />
                  <Text className="font-inter text-xs text-muted">{[`${Math.floor(q.pct / 10)}%`, lane && lane.eta ? `${dur(lane.eta)} left` : '', q.note].filter(Boolean).join(' · ')}</Text>
                </View>
              )) : <Text className="font-inter text-sm text-muted">Nothing is downloading.</Text>}
              {queue.length > now.length ? <Text className="font-inter text-sm text-muted">{`${queue.length - now.length} waiting or paused`}</Text> : null}
              <Button small label="Open downloads" onPress={() => setTab('dl')} />
            </Card>
            <Cleanup onChanged={onChanged} ask={setAsking} />
          </View>
        </ScrollView>
      </FocusScope>
      {asking ? <Confirm title={asking.title} body={asking.body} yes={asking.yes} onYes={asking.onYes} onClose={() => setAsking(null)} /> : null}
    </View>
  );
}

// ---------- emulators: the PC's ROM library ----------

// a ROM has no art: a tile in the system's own shade of blue, with its name
const SHADES = [['#2F5BD8', '#0A1640'], ['#4B9CD3', '#0B2238'], ['#3A47B8', '#0D0F33'], ['#2B7FB8', '#08192B'], ['#5468E8', '#111640']];
function shade(system) {
  let h = 0;
  for (const ch of system) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return SHADES[h % SHADES.length];
}

function RomArt({rom, prog}) {
  const focused = useIsFocused();
  const [from, to] = shade(rom.system);
  return (
    <View focused={focused} className="w-[176px] h-[176px] rounded-2xl border-[3px] border-transparent overflow-hidden transition focused:border-accent focused:scale-105"
      style={{backgroundGradient: {type: 'linear', angle: 135, stops: [{color: from, offset: 0}, {color: to, offset: 1}]}}}>
      <View className="flex-1 p-[14px] justify-between">
        <Text className="font-inter text-xs font-bold tracking-widest text-text/70">{rom.system.toUpperCase()}</Text>
        <Text className={`${T} text-base font-bold line-clamp-4`}>{rom.name}</Text>
        <Text className="font-inter text-xs text-text/70">{rom.isDir ? 'Folder' : (rom.file.match(/\.([^.]+)$/) || ['', ''])[1].toUpperCase()}</Text>
      </View>
      {busy(prog) ? <View className="absolute left-[10px] top-[10px] px-[10px] py-[3px] rounded-full bg-ink/90"><Text className="font-inter text-xs text-accent">{WORD[prog.state] || 'Downloading'}</Text></View> : null}
      {prog && prog.state === 'sent' ? <View className="absolute right-[10px] top-[10px] px-[10px] py-[3px] rounded-full bg-ink/90"><Text className="font-inter text-xs text-mint">On PS5</Text></View> : null}
    </View>
  );
}

const RomTile = memo(function RomTile({rom, prog, onPick, onSpot, end}) {
  return (
    <View onPress={() => onPick(rom)} onFocus={() => onSpot(rom.key)} focusKey={end} nextFocusRight={end} className="w-[176px] gap-[8px]">
      <RomArt rom={rom} prog={prog} />
      <TileName name={rom.name} />
    </View>
  );
});

function Emulators({progress, onPick}) {
  const [data, setData] = useState(null);
  const [error, setError] = useState('');
  const [spot, setSpot] = useState(null);
  useEffect(() => {
    let stop = false;
    api.roms().then(d => { if (!stop) setData(d); }).catch(err => { if (!stop) setError(err.message); });
    return () => { stop = true; };
  }, []);
  const onSpot = useCallback(key => setSpot(key), []);
  const systems = (data && data.systems) || [];
  const all = systems.flatMap(s => s.games);
  const rom = all.find(r => r.key === spot) || all[0];
  const prog = rom && progress[`rom:${rom.key}`];
  let empty = '';
  if (error) empty = error;
  else if (!data) empty = 'Reading your ROM library…';
  else if (!data.folder) empty = 'No ROM library yet. In Loadout on the PC, open Emulators and click ROM library, then pick the folder with your ROMs: one folder per system inside it (Switch for ProsperoEden; anything else goes to RetroArch).';
  else if (!systems.length) empty = `No games in ${data.folder} yet. Put each system's games in a folder of its own inside it.`;
  return (
    <View className="flex-1">
      <View className="h-[258px] px-[64px] pt-[22px] gap-[8px] w-[800px]">
        <Text className="font-inter text-xs text-muted tracking-[4px]">EMULATORS</Text>
        {rom ? <Text className={`${T} text-[38px] leading-[46px] font-bold line-clamp-2`}>{rom.name}</Text> : null}
        {rom ? <Text className="font-inter text-sm text-muted">{[rom.system, size(rom.size), rom.file].filter(Boolean).join('   ·   ')}</Text> : null}
        {rom ? <Text className="font-inter text-sm text-accent">{busy(prog) ? `${WORD[prog.state] || 'Downloading'}${prog.pct ? ` · ${Math.floor(prog.pct / 10)}%` : ''}` : prog && prog.state === 'sent' ? `Downloaded to ${rom.emu}` : `Downloads to ${rom.emu} on this PS5`}</Text> : null}
        {!rom ? <Text className={`${T} text-[38px] leading-[46px] font-bold`}>Emulators</Text> : null}
        {!rom ? <Text className="font-inter text-sm text-muted">Games from the ROM library on your PC, sent straight to the right emulator.</Text> : null}
      </View>
      {empty ? (
        <FocusScope autoFocus>
          <View className="px-[64px] pt-[10px] w-[900px] gap-[16px]">
            <Text className="font-inter text-base text-muted">{empty}</Text>
            <View focusable className="w-[1px] h-[1px]" />
          </View>
        </FocusScope>
      ) : (
        <FocusScope autoFocus>
          <ScrollView className="flex-1">
            {systems.map(s => {
              const last = s.games.length - 1;
              return (
                <View key={s.name} scrollAnchor className="gap-[4px] pt-[16px]">
                  <Heading title={s.name} note={`${s.games.length} · to ${s.emu}`} />
                  <ScrollView horizontal className="w-screen h-[235px] flex-row items-start gap-[22px] py-[12px]">
                    <View className="w-[42px] h-[1px]" />
                    {s.games.map((r, i) => <RomTile key={r.key} rom={r} prog={progress[`rom:${r.key}`]} onPick={onPick} onSpot={onSpot} end={i === last ? `end:rom:${s.name}` : undefined} />)}
                    <View className="w-[42px] h-[1px]" />
                  </ScrollView>
                </View>
              );
            })}
            <View className="h-[40px]" />
          </ScrollView>
        </FocusScope>
      )}
    </View>
  );
}

// ---------- homebrew: the homebrew.page catalog, ProsperoStore's ----------

const HB_SHOW = [{label: 'All apps'}, {label: 'Installed', test: a => !!a.installed}, {label: 'Updates', test: a => a.update},
  {label: 'Not installed', test: a => !a.installed && a.installable}];
const SANDBOX = {stays: 'Stays in the PS5 sandbox', leaves: 'Can get full access to the console', unclear: 'Sandbox use unclear'};

function HbArt({app, prog}) {
  const focused = useIsFocused();
  return (
    <View focused={focused} className="w-[176px] h-[176px] rounded-2xl bg-raised border-[3px] border-transparent overflow-hidden
      transition focused:border-accent focused:scale-105 items-center justify-center">
      {app.icon
        ? <Image source={{uri: app.icon}} resizeMode="contain" className="w-[150px] h-[150px] rounded-xl" />
        : <Text className={`${T} text-base text-center px-[12px] line-clamp-3`}>{app.name}</Text>}
      {busy(prog) ? (
        <View className="absolute right-[10px] top-[10px] px-[10px] py-[3px] rounded-lg bg-black/75">
          <Text className="font-inter text-xs text-accent">{prog.pct ? `${Math.floor(prog.pct / 10)}%` : 'Working'}</Text>
        </View>
      ) : app.update ? (
        <View className="absolute right-[10px] top-[10px] px-[10px] py-[3px] rounded-lg bg-accent">
          <Text className="font-inter text-xs font-bold text-ink">Update</Text>
        </View>
      ) : app.installed ? (
        <View className="absolute right-[10px] top-[10px] px-[10px] py-[3px] rounded-lg bg-black/75">
          <Text className="font-inter text-xs text-sky">Installed</Text>
        </View>
      ) : null}
    </View>
  );
}

const HbTile = memo(function HbTile({app, prog, onPick}) {
  return (
    <View onPress={() => onPick(app)} className="w-[176px] gap-[8px]">
      <HbArt app={app} prog={prog} />
      <TileName name={app.name} />
    </View>
  );
});

// one app: what it is and what can be done with it
function HbPage({app, prog, onClose, onDone}) {
  const [info, setInfo] = useState(null);
  const [note, setNote] = useState(null);
  const [asking, setAsking] = useState(null);
  useEffect(() => {
    let stop = false;
    api.homebrewApp(app.id).then(r => { if (!stop) setInfo(r.data || {}); }).catch(err => { if (!stop) setNote({text: err.message, bad: true}); });
    return () => { stop = true; };
  }, [app.id]);
  const act = async action => {
    try {
      const r = await api.homebrewDo(app.id, action);
      setNote({text: (r.data && r.data.msg) || 'Started.'});
      onDone();
    } catch (err) {
      setNote({text: err.message, bad: true});
    }
  };
  const b = busy(prog);
  const main = !app.installable ? null : app.update ? 'Update' : app.installed ? 'Reinstall' : 'Install';
  const meta = [app.author && `by ${app.author}`, `Version ${app.version}`, size(app.size), app.installed && app.installed !== 'installed' ? `you have ${app.installed}` : ''].filter(Boolean).join('   ·   ');
  const safety = info && info.safety ? SANDBOX[info.safety.sandbox] : SANDBOX[app.sandbox];
  return (
    <FocusScope autoFocus trap onBack={() => { onClose(); return true; }}>
      <View className="absolute inset-0 w-screen h-screen bg-black/80 items-center justify-center">
        <View className="w-[900px] p-[34px] gap-[14px] rounded-3xl bg-surface border-2 border-royal">
          <View className="flex-row gap-[24px]">
            {app.icon ? <Image source={{uri: app.icon}} resizeMode="contain" className="w-[120px] h-[120px] rounded-2xl" /> : null}
            <View className="flex-1 gap-[6px]">
              <Text className={`${T} text-[32px] leading-[40px] font-bold line-clamp-2`}>{app.name}</Text>
              <Text className="font-inter text-sm text-muted">{meta}</Text>
              {safety ? <Text className="font-inter text-sm" style={{color: app.sandbox === 'leaves' ? C.coral : C.mint}}>{safety}</Text> : null}
            </View>
          </View>
          {info && readable(info.description) ? <Text className="font-inter text-sm text-text/85 line-clamp-5">{info.description}</Text>
            : !info ? <Text className="font-inter text-sm text-muted">Reading the catalog…</Text> : null}
          {b ? (
            <View className="gap-[6px]">
              <Text className="font-inter text-sm text-text">{prog.note || 'Working'}</Text>
              <Bar pct={(prog.pct || 0) / 10} width={830} />
            </View>
          ) : null}
          {prog && prog.state === 'sent' && prog.note ? <Text className="font-inter text-sm text-sky">{prog.note}</Text> : null}
          {prog && prog.state === 'failed' ? <Text className="font-inter text-sm text-coral">{prog.note}</Text> : null}
          {note ? <Text className="font-inter text-sm" style={{color: note.bad ? C.coral : C.sky}}>{note.text}</Text> : null}
          <View className="flex-row gap-[14px] pt-[6px]">
            {main ? <Button primary label={main} disabled={b} onPress={() => act('install')} /> : <Button label="Not installable here" disabled />}
            {app.installed && !b ? <Button label="Remove" onPress={() => setAsking(true)} /> : null}
            <Button label="Close" onPress={onClose} />
          </View>
          <Text className="font-inter text-xs text-faint">{app.source === 'GitHub'
            ? `Version ${app.version} from the app's GitHub releases (newer than homebrew.page lists), checked against the release's checksum. ${app.id}`
            : `From homebrew.page, checked against its signed catalog. ${app.id}`}</Text>
        </View>
      </View>
      {asking ? <Confirm title={`Remove ${app.name}?`} yes="Remove" body="Its folder is deleted from the PS5. The app's own saved data is left alone."
        onYes={() => act('uninstall')} onClose={() => setAsking(false)} /> : null}
    </FocusScope>
  );
}

function Homebrew({progress, onChanged}) {
  const [apps, setApps] = useState(null);
  const [error, setError] = useState('');
  const [show, setShow] = useState(0);
  const [pick, setPick] = useState(null);
  const load = useCallback(refresh => {
    api.homebrew(refresh).then(r => { setApps((r.data && r.data.apps) || []); setError(''); }).catch(err => setError(err.message));
  }, []);
  useEffect(() => { load(false); }, [load]);
  // what's installed changes when a job finishes
  const running = Object.keys(progress).filter(k => k.startsWith('hb:') && busy(progress[k])).length;
  const was = useRef(0);
  useEffect(() => {
    if (was.current && !running) load(false);
    was.current = running;
  }, [running, load]);
  const all = (apps || []).filter(a => a.status === 'available');
  const list = HB_SHOW[show].test ? all.filter(HB_SHOW[show].test) : all;
  const updates = all.filter(a => a.update).length;
  const renderItem = useCallback(({item}) => <HbTile app={item} prog={progress[`hb:${item.id}`]} onPick={setPick} />, [progress]);
  const picked = pick && (apps || []).find(a => a.id === pick.id);
  return (
    <View className="flex-1">
      <PageTitle kicker="HOMEBREW" title="Homebrew apps"
        note={error || (apps ? `${all.length} apps from homebrew.page${updates ? ` · ${updates} update${updates === 1 ? '' : 's'}` : ''}` : 'Reading the catalog…')} />
      <FocusScope autoFocus>
        <ScrollView className="flex-1">
          <View scrollAnchor className="flex-row items-center gap-[10px] px-[64px] pt-[14px]">
            {HB_SHOW.map((s, i) => <Pill key={s.label} label={s.label} on={show === i} onPress={() => setShow(i)} />)}
            <Pill label="Refresh" onPress={() => load(true)} />
          </View>
          <View scrollAnchor className="px-[64px] pt-[16px] pb-[70px]">
            {list.length ? (
              <VirtualList data={list} keyExtractor={a => a.id} renderItem={renderItem}
                numColumns={6} itemHeight={TILE_H} rowGap={26} columnGap={18} />
            ) : <Text className="font-inter text-base text-muted">{apps ? 'Nothing here.' : ' '}</Text>}
          </View>
        </ScrollView>
      </FocusScope>
      {picked ? <HbPage app={picked} prog={progress[`hb:${picked.id}`]} onClose={() => setPick(null)} onDone={onChanged} /> : null}
    </View>
  );
}

// on Your PS5: what stopped copies left on the drives
function Cleanup({onChanged, ask}) {
  const [scan, setScan] = useState(null);
  const [busyNow, setBusyNow] = useState(false);
  const [note, setNote] = useState(null);
  const run = async () => {
    setBusyNow(true);
    setNote(null);
    try {
      const r = await api.cleanupScan();
      setScan(r.data || {items: []});
    } catch (err) {
      setNote({text: err.message, bad: true});
    }
    setBusyNow(false);
  };
  const items = (scan && scan.items) || [];
  const total = items.reduce((n, it) => n + (it.size > 0 ? it.size : 0), 0);
  const remove = async () => {
    setBusyNow(true);
    try {
      const r = await api.cleanupDelete(items.map(it => it.path));
      setNote({text: (r.data && r.data.msg) || 'Done.'});
      setScan(null);
      onChanged();
    } catch (err) {
      setNote({text: err.message, bad: true});
    }
    setBusyNow(false);
  };
  return (
    <Card title="Clean up drives" right={scan ? `${items.length} found` : ''}>
      <Text className="font-inter text-sm text-muted">Finds what stopped copies leave behind: unfinished uploads, broken game folders, empty images and Loadout's leftovers.</Text>
      {items.slice(0, 5).map(it => (
        <View key={it.path} className="gap-[1px]">
          <Text className={`${T} text-sm truncate w-[500px]`}>{it.path.split('/').pop()}</Text>
          <Text className="font-inter text-xs text-muted truncate w-[500px]">{[it.reason, it.drive, it.size > 0 ? size(it.size) : ''].filter(Boolean).join(' · ')}</Text>
        </View>
      ))}
      {items.length > 5 ? <Text className="font-inter text-xs text-muted">{`and ${items.length - 5} more`}</Text> : null}
      {scan && !items.length ? <Text className="font-inter text-sm text-mint">Nothing to clean up.</Text> : null}
      {scan && scan.kept ? <Text className="font-inter text-xs text-faint">{`${scan.kept} unfinished file${scan.kept === 1 ? ' is' : 's are'} kept: a queued or paused download still needs ${scan.kept === 1 ? 'it' : 'them'}.`}</Text> : null}
      {note ? <Text className="font-inter text-sm" style={{color: note.bad ? C.coral : C.sky}}>{note.text}</Text> : null}
      <View className="flex-row gap-[12px]">
        <Button small label={busyNow ? 'Working…' : scan ? 'Scan again' : 'Scan the drives'} disabled={busyNow} onPress={run} />
        {items.length ? <Button small primary label={`Delete ${items.length}${total ? ` (${size(total)})` : ''}`} disabled={busyNow} onPress={() => ask({title: `Delete ${items.length} item${items.length === 1 ? '' : 's'}?`, yes: 'Delete',
          body: `They're deleted from the PS5${total ? `, freeing ${size(total)}` : ''}. This can't be undone.`, onYes: remove})} /> : null}
      </View>
    </Card>
  );
}

// the PS5 queue, one download at a time
const QSTATE = {queued: 'Waiting', paused: 'Paused', sending: 'Downloading', copying: 'Copying', moving: 'Moving',
  deleting: 'Deleting', saving: 'Backing up', restoring: 'Restoring'};

function Menu({title, items, onClose}) {
  return (
    <FocusScope autoFocus trap onBack={() => { onClose(); return true; }}>
      <View className="absolute inset-0 w-screen h-screen bg-black/70 items-center justify-center">
        <View className="w-[520px] p-[28px] gap-[12px] rounded-3xl bg-surface border-2 border-royal">
          <Text className={`${T} text-xl font-bold line-clamp-2`}>{title}</Text>
          {items.map(it => <Button key={it.label} label={it.label} primary={it.primary} onPress={() => { onClose(); it.onPress(); }} />)}
          <Button label="Cancel" onPress={onClose} />
        </View>
      </View>
    </FocusScope>
  );
}

function Downloads({status, onChanged}) {
  const [menu, setMenu] = useState(null);
  const [note, setNote] = useState(null);
  const queue = (status && status.queue) || [];
  const act = async (q, action) => {
    try {
      await api.job(q.id, action);
      setNote(null);
      onChanged();
    } catch (err) {
      setNote({text: err.message, bad: true});
    }
  };
  const choices = q => {
    const running = q.state !== 'queued' && q.state !== 'paused';
    return [
      q.state === 'queued' && q.waits > 1 ? {label: 'Move up', onPress: () => act(q, 'up')} : null,
      q.state === 'queued' ? {label: 'Move down', onPress: () => act(q, 'down')} : null,
      running ? {label: 'Pause', onPress: () => act(q, 'pause')} : null,
      q.state === 'paused' ? {label: 'Resume', primary: true, onPress: () => act(q, 'resume')} : null,
      {label: 'Stop this download', onPress: () => act(q, 'stop')},
    ].filter(Boolean);
  };
  return (
    <View className="flex-1">
      <PageTitle kicker="DOWNLOADS" title="Downloads"
        note={queue.length ? 'Press a download to pause it, move it in the line, or stop just that one.' : 'Nothing is downloading or waiting.'} />
      {note ? <Text className="font-inter text-sm px-[64px] pt-[6px]" style={{color: note.bad ? C.coral : C.sky}}>{note.text}</Text> : null}
      <FocusScope autoFocus>
        <ScrollView className="flex-1">
          <View className="gap-[10px] px-[64px] pt-[16px] pb-[40px]">
            {queue.length ? queue.map(q => {
              const sub = [QSTATE[q.state] || q.state, q.state !== 'queued' && q.state !== 'paused' && q.pct ? `${Math.floor(q.pct / 10)}%` : '',
                q.waits ? (q.waits === 1 ? 'next in line' : `${q.waits} in line`) : '', q.note].filter(Boolean).join('   ·   ');
              return <Row key={q.id} title={q.name} sub={sub} action="Options" onPress={() => setMenu(q)} />;
            }) : <Row title="Your downloads show here" sub="Download a game from Discover or Browse" />}
          </View>
        </ScrollView>
      </FocusScope>
      {menu ? <Menu title={menu.name} items={choices(menu)} onClose={() => setMenu(null)} /> : null}
    </View>
  );
}

const TABS = ['games', 'browse', 'library', 'brew', 'emu', 'dl', 'console'];

const HINTS = {
  games: [{btn: 'cross', label: 'Open game'}, {btn: 'L1 R1', label: 'Tabs'}],
  browse: [{btn: 'cross', label: 'Open game'}, {btn: 'L1 R1', label: 'Tabs'}],
  library: [{btn: 'cross', label: 'Open game'}, {btn: 'L1 R1', label: 'Tabs'}],
  brew: [{btn: 'cross', label: 'Open app'}, {btn: 'L1 R1', label: 'Tabs'}],
  emu: [{btn: 'cross', label: 'Download'}, {btn: 'L1 R1', label: 'Tabs'}],
  dl: [{btn: 'cross', label: 'Options'}, {btn: 'L1 R1', label: 'Tabs'}],
  console: [{btn: 'cross', label: 'Select'}, {btn: 'L1 R1', label: 'Tabs'}],
};

function Home({games, status, progress, onOpen, onPickRom, tab, setTab, update, onUpdate, onChanged, drive, setDrive}) {
  const flip = useCallback(action => {
    if (action === 'triangle' && update) {
      onUpdate();
      return true;
    }
    if (action !== 'l1' && action !== 'r1') return false;
    setTab(t => TABS[(TABS.indexOf(t) + (action === 'r1' ? 1 : TABS.length - 1)) % TABS.length]);
    return true;
  }, [setTab, update, onUpdate]);
  return (
    <FocusScope onAction={flip}>
      <View className="w-screen h-screen bg-ink">
        {tab === 'games' ? <BackdropFor games={games} /> : <View className="absolute inset-0 bg-gradient-to-b from-ink2 via-ink to-black" />}
        <TopBar tab={tab} setTab={setTab} status={status} update={update} />
        {tab === 'games' ? <Games key="games" games={games} progress={progress} onOpen={onOpen} />
          : tab === 'browse' ? <Browse key="browse" games={games} progress={progress} onOpen={onOpen} />
            : tab === 'library' ? <Library key="library" games={games} progress={progress} onOpen={onOpen} />
              : tab === 'brew' ? <Homebrew key="brew" progress={progress} onChanged={onChanged} />
              : tab === 'emu' ? <Emulators key="emu" progress={progress} onPick={onPickRom} />
                : tab === 'dl' ? <Downloads key="dl" status={status} onChanged={onChanged} />
                  : <Console key="console" status={status} drive={drive} setDrive={setDrive} setTab={setTab} onChanged={onChanged} />}
        <Hints items={HINTS[tab] || HINTS.games} />
      </View>
    </FocusScope>
  );
}

// the home backdrop follows the spotlight; Games reports it through this tiny store
const spotStore = {key: null, listeners: new Set()};
function BackdropFor({games}) {
  const [key, setKey] = useState(spotStore.key);
  useEffect(() => {
    const fn = k => setKey(k);
    spotStore.listeners.add(fn);
    return () => spotStore.listeners.delete(fn);
  }, []);
  const g = games.find(x => x.key === key) || games[0];
  return <Backdrop hero={g && g.hero} />;
}
const tellSpot = key => { spotStore.key = key; for (const fn of spotStore.listeners) fn(key); };

function Shot({k, n}) {
  const focused = useIsFocused();
  return (
    <View focused={focused} className="w-[352px] h-[198px] rounded-xl bg-raised border-[3px] border-transparent overflow-hidden transition focused:border-accent focused:scale-105">
      <Image source={{uri: art(k, `shot${n}`)}} resizeMode="cover" className="w-[346px] h-[192px] rounded-lg bg-raised" />
    </View>
  );
}

// a screenshot over the whole screen; Left and Right page through them
function Viewer({k, count, start, onClose}) {
  const [n, setN] = useState(start);
  useController(action => {
    if (action === 'left') setN(i => (i + count - 1) % count);
    if (action === 'right') setN(i => (i + 1) % count);
  });
  return (
    <FocusScope autoFocus trap onBack={() => { onClose(); return true; }}>
      <View focusable onPress={onClose} className="absolute inset-0 w-screen h-screen bg-black items-center justify-center">
        <Image key={n} source={{uri: art(k, `shot${n}`)}} resizeMode="contain" className="w-screen h-screen" />
        <View className="absolute bottom-[24px] px-[16px] py-[6px] rounded-full bg-ink/80">
          <Text className="font-inter text-sm text-text">{`${n + 1} of ${count}   ·   Left / Right   ·   Circle to close`}</Text>
        </View>
      </View>
    </FocusScope>
  );
}

// a row in the game page's DLC and saves lists. The whole row is the button,
// with its action at the right, so Down from the buttons above lands on it
// (a small button at the far right was too far off to the side to be picked).
function RowText({title, sub, tag}) {
  return (
    <View className="flex-1 gap-[2px]">
      <View className="flex-row items-center gap-[10px]">
        <Text className={`${T} text-base truncate`}>{title}</Text>
        {tag ? (
          <View className="px-[10px] py-[2px] rounded-full bg-mint/20 border border-mint">
            <Text className="font-inter text-xs font-bold text-mint">{tag}</Text>
          </View>
        ) : null}
      </View>
      {sub ? <Text className="font-inter text-xs text-muted truncate">{sub}</Text> : null}
    </View>
  );
}

function Row({title, sub, tag, action, primary, onPress}) {
  return onPress ? (
    <View onPress={onPress} className="w-[1150px] flex-row items-center gap-[16px] px-[18px] py-[10px] rounded-xl bg-surface/85 border-[3px] border-line focused:border-accent">
      <RowText title={title} sub={sub} tag={tag} />
      {action ? (
        <View className={primary ? 'px-[18px] py-[6px] rounded-lg bg-accent' : 'px-[18px] py-[6px] rounded-lg bg-raised'}>
          <Text className={primary ? 'font-inter text-sm font-bold text-ink' : 'font-inter text-sm text-text'}>{action}</Text>
        </View>
      ) : null}
    </View>
  ) : (
    <View focusable className="w-[1150px] flex-row items-center gap-[16px] px-[18px] py-[10px] rounded-xl bg-surface/85 border-[3px] border-line focused:border-text">
      <RowText title={title} sub={sub} tag={tag} />
      {action ? <Text className="font-inter text-sm text-faint">{action}</Text> : null}
    </View>
  );
}

const dlcDone = game => (game.dlc || []).filter(d => d.installed).length;

// where a DLC install is; the PC copies the package over and installs it
function dlcLine(p) {
  if (!p) return {text: '', busy: false};
  if (p.state === 'sent') return {text: 'Installed', busy: false, done: true};
  if (p.state === 'failed') return {text: `Failed: ${p.note}`, busy: false};
  if (p.state === 'cancelled') return {text: 'Stopped', busy: false};
  if (p.state === 'queued') return {text: 'Waiting for the PS5…', busy: true};
  return {text: `${p.note || 'Installing'}${p.pct ? ` ${Math.floor(p.pct / 10)}%` : '…'}`, busy: true};
}

function DLCSection({game, progress, installs, startDLC, say, ask}) {
  const list = game.dlc || [];
  if (!list.length) return null;
  const install = async d => {
    say({text: `Asking your PC to install ${d.name}…`});
    try {
      say({text: await startDLC(d)});
    } catch (err) {
      say({text: err.message, bad: true});
    }
  };
  return (
    <View scrollAnchor className="gap-[10px] pt-[26px] px-[64px]">
      <Heading title="DLC" note={`${list.length} on your PC${dlcDone(game) ? ` · ${dlcDone(game)} installed` : ''}`} pad={0} />
      {list.map(d => {
        const line = dlcLine(progress[installs[d.key]]);
        const on = d.installed || !!line.done;
        const sub = [d.kind, size(d.size), line.done ? '' : line.text, d.pkg || on ? '' : 'Only .pkg DLC installs from here'].filter(Boolean).join('   ·   ');
        const tag = line.busy ? '' : on ? 'INSTALLED' : '';
        return d.pkg && !line.busy ? (
          <Row key={d.key} title={d.name} sub={sub} tag={tag} action={on ? 'Reinstall' : 'Install'} primary={!on}
            onPress={() => ask({title: `Install ${d.name}?`, yes: 'Install', onYes: () => install(d),
              body: 'Your PC copies the package to the PS5 and installs it. You can leave this page; the progress shows here and on the PC.'})} />
        ) : (
          <Row key={d.key} title={d.name} sub={sub} tag={tag} action={line.busy ? 'Installing…' : on ? '' : 'Can’t install'} />
        );
      })}
    </View>
  );
}

function SavesSection({game, progress, say, ask}) {
  const [data, setData] = useState(null);
  const id = game.id;
  const load = useCallback(() => api.saves(id).then(setData).catch(() => {}), [id]);
  // the PC reads the PS5's saves in the background; ask again until it's done
  useEffect(() => {
    if (!id) return undefined;
    load();
    const t = setInterval(load, 4000);
    return () => clearInterval(t);
  }, [id, load]);
  if (!id) return null;
  const saves = (data && data.saves) || [], backups = (data && data.backups) || [];
  const moving = Object.keys(progress).some(k => (k.startsWith('save:') || k.startsWith('rest:')) && k.includes(id) && busy(progress[k]));
  const backup = async () => {
    say({text: 'Backing up your save to the PC…'});
    try {
      await api.saveBackup(id, game.name);
      say({text: 'Backing up. It shows below when it’s done.'});
    } catch (err) {
      say({text: err.message, bad: true});
    }
  };
  const restore = async b => {
    say({text: 'Restoring the save…'});
    try {
      await api.saveRestore(b.id);
      say({text: 'Restoring. Your current save is backed up first, just in case.'});
    } catch (err) {
      say({text: err.message, bad: true});
    }
  };
  const onPs5 = !data ? 'Looking for this game’s save on your PS5…'
    : saves.length ? saves.map(s => `${s.userName || s.user}: ${size(s.size)}`).join('   ·   ')
      : data.busy ? 'Looking for this game’s save on your PS5…' : data.err ? `Can't read saves: ${data.err}` : 'No save for this game on your PS5';
  return (
    <View scrollAnchor className="gap-[10px] pt-[26px] px-[64px]">
      <Heading title="Saves" note={backups.length ? `${backups.length} backup${backups.length === 1 ? '' : 's'} on your PC` : 'none backed up yet'} pad={0} />
      {saves.length && !moving
        ? <Row title="On your PS5" sub={onPs5} action="Back up to PC" primary onPress={backup} />
        : <Row title="On your PS5" sub={onPs5} action={moving ? 'Working…' : ''} />}
      {backups.slice(0, 8).map(b => {
        const sub = [b.userName || b.user, size(b.size), b.auto ? `safety copy before a ${b.why || 'change'}` : ''].filter(Boolean).join('   ·   ');
        return moving ? <Row key={b.id} title={when(b.when) || b.id} sub={sub} action="Working…" /> : (
          <Row key={b.id} title={when(b.when) || b.id} sub={sub} action="Restore"
            onPress={() => ask({title: 'Restore this save?', yes: 'Restore', onYes: () => restore(b),
              body: `It replaces the save for ${game.name} on your PS5 with the one from ${when(b.when)}. Close the game completely first (not just suspended). Your current save is backed up to your PC first.`})} />
        );
      })}
    </View>
  );
}

function Detail({game, status, progress, installs, startDLC, drive, setDrive, onBack, onDownload, message}) {
  const [shot, setShot] = useState(-1);
  const [note, setNote] = useState(null);
  // a question from the DLC or saves lists, drawn over the whole page
  const [asking, setAsking] = useState(null);
  const prog = progress[game.key];
  const hero = art(game.hero, 'hero');
  const cover = art(game.cover, 'cover');
  const drives = (status && status.drives) || [];
  const b = busy(prog);
  const st = stateOf(game, prog);
  const by = [game.publisher, game.release].filter(Boolean).join('   ·   ');
  const chips = [game.platform, game.version && `v${game.version}`, game.outdated && `Update ${game.latest} available`, size(game.size), ...(game.genres || []).slice(0, 3), game.rated, game.id].filter(Boolean);
  const where = game.onPs5 ? `On your PS5 · ${game.onPs5}` : st ? st.label : '';
  const label = b ? `${WORD[prog.state] || prog.state}…` : game.onPs5 ? 'Download again' : 'Download';
  const shots = game.lib ? Math.min(game.shots || 0, 6) : 0;
  const shown = note || message;
  const fav = useFavs().has(game.key);
  return (
    <FocusScope autoFocus trap onBack={() => { onBack(); return true; }}>
      <View className="w-screen h-screen bg-ink">
        {hero ? <Image source={{uri: hero}} resizeMode="cover" className="absolute inset-0 w-screen h-screen" /> : null}
        <View className="absolute inset-0 bg-gradient-to-r from-ink via-ink/85 to-ink/30" />
        <View className="absolute left-0 bottom-0 w-screen h-[380px] bg-gradient-to-t from-ink via-ink/90 to-transparent" />
        <ScrollView className="flex-1">
          <View scrollAnchor className="px-[64px] pt-[52px] gap-[18px]">
            <View className="flex-row gap-[32px]">
              {cover
                ? <Image source={{uri: cover}} resizeMode="cover" className="w-[230px] h-[230px] rounded-3xl bg-raised" />
                : <View className="w-[230px] h-[230px] rounded-3xl bg-raised" />}
              <View className="w-[880px] gap-[10px]">
                <Text className={`${T} text-[40px] leading-[48px] font-bold line-clamp-2`}>{game.name}</Text>
                {by ? <Text className="font-inter text-base text-muted">{by}</Text> : null}
                {game.rating ? (
                  <View className="flex-row items-end gap-[10px]">
                    <Text className="font-inter text-base font-bold" style={{color: C.accent}}>{`★ ${game.rating.toFixed(1)}`}</Text>
                    {game.ratings ? <Text className="font-inter text-sm text-muted">{`${game.ratings} ratings`}</Text> : null}
                  </View>
                ) : null}
                <View className="flex-row flex-wrap gap-[8px] pt-[2px]">{chips.map(c => <Chip key={c}>{c}</Chip>)}</View>
                {readable(game.desc) ? <Text className="font-inter text-sm text-text/85 line-clamp-4 pt-[4px]">{game.desc}</Text> : null}
              </View>
            </View>
            <View className="flex-row items-center gap-[16px] pt-[6px]">
              <Button primary label={label} onPress={() => { setNote(null); onDownload(); }} disabled={b || !game.canGet} />
              <Button label={fav ? '★ Favourite' : 'Add to favourites'} onPress={() => toggleFav(game.key)} />
              <Button label="Back" onPress={onBack} />
              {where ? <Text className="font-inter text-sm pl-[8px] truncate w-[420px]" style={{color: st ? st.color : C.muted}}>{where}</Text> : null}
            </View>
            {drives.length ? (
              <View className="gap-[8px]">
                <Text className="font-inter text-xs text-faint tracking-widest">DOWNLOAD TO</Text>
                <View className="flex-row gap-[12px]">
                  {drives.map(d => (
                    <View key={d.root} onPress={() => setDrive(d.root)} selected={d.root === drive}
                      className="px-[18px] py-[9px] rounded-xl bg-surface/90 border-[3px] border-line selected:border-accent focused:border-text">
                      <Text className="font-inter text-sm text-text">{d.short || d.label}</Text>
                      <Text className="font-inter text-xs text-muted">{d.free >= 0 ? `${size(d.free)} free` : d.label}</Text>
                    </View>
                  ))}
                </View>
              </View>
            ) : null}
            {b || (prog && prog.state) ? (
              <View className="w-[600px] gap-[6px]">
                <Text className={`${T} text-base`}>{WORD[prog.state] || ''}</Text>
                <Bar pct={prog.state === 'sent' ? 100 : (prog.pct || 0) / 10} width={600} color={prog.state === 'failed' ? C.coral : C.accent} />
                {prog.note ? <Text className="font-inter text-sm text-muted">{prog.note}</Text> : null}
              </View>
            ) : null}
            {shown ? <Text className="font-inter text-sm" style={{color: shown.bad ? C.coral : C.sky}}>{shown.text}</Text> : null}
            <BackportSection game={game} progress={progress} say={setNote} ask={setAsking} />
          </View>
          <DLCSection game={game} progress={progress} installs={installs} startDLC={d => startDLC(game, d)} say={setNote} ask={setAsking} />
          <SavesSection game={game} progress={progress} say={setNote} ask={setAsking} />
          {shots ? (
            <View scrollAnchor className="gap-[4px] pt-[28px]">
              <Heading title="Screenshots" pad={64} />
              <ScrollView horizontal className="w-screen h-[230px] flex-row items-start gap-[20px] py-[12px]">
                <View className="w-[44px] h-[1px]" />
                {Array.from({length: shots}, (_, n) => (
                  <View key={n} onPress={() => setShot(n)} focusKey={n === shots - 1 ? 'end:shots' : undefined} nextFocusRight={n === shots - 1 ? 'end:shots' : undefined}><Shot k={game.lib} n={n} /></View>
                ))}
                <View className="w-[44px] h-[1px]" />
              </ScrollView>
            </View>
          ) : null}
          <View className="h-[60px]" />
        </ScrollView>
        {shot >= 0 ? <Viewer k={game.lib} count={shots} start={shot} onClose={() => setShot(-1)} /> : null}
        {asking ? <Confirm title={asking.title} body={asking.body} yes={asking.yes} onYes={asking.onYes} onClose={() => setAsking(null)} /> : null}
      </View>
    </FocusScope>
  );
}

// a game on this PS5 as a folder that won't start on this firmware: the PC
// backports it in place (its programs, and the firmware its param.json asks for)
function BackportSection({game, progress, say, ask}) {
  const folder = (game.ps5Folders || [])[0];
  if (!folder) return null;
  const needs = game.bpState === 'needs' || game.bpState === 'incomplete';
  const p = progress[`ps5bp:${folder}`];
  const b = busy(p);
  const start = async () => {
    try {
      const r = await api.ps5Backport(folder);
      say({text: (r.data && r.data.msg) || 'Backporting it.'});
    } catch (err) {
      say({text: err.message, bad: true});
    }
  };
  return (
    <View className={needs ? 'w-[1000px] gap-[8px] p-[18px] rounded-2xl border-2 border-coral bg-surface/90' : 'w-[1000px] gap-[8px] p-[18px] rounded-2xl border border-line bg-surface/80'}>
      <Text className={`${T} text-base font-bold`}>{game.bpState === 'incomplete' ? "This backport wasn't finished" : needs ? 'This game needs a backport' : 'Backport on this PS5'}</Text>
      <Text className="font-inter text-sm text-muted">{needs ? game.bpNote
        : "Its programs already ask for your firmware or older. If it still closes as soon as it starts, a backport lowers the firmware its param.json asks for and adds fakelib files from your PC (pick them in Loadout's Settings), which some games need."}</Text>
      {b ? (
        <View className="gap-[6px]">
          <Text className="font-inter text-sm text-text">{p.note || 'Backporting'}</Text>
          <Bar pct={(p.pct || 0) / 10} width={960} color={C.coral} />
        </View>
      ) : p && p.state === 'sent' ? <Text className="font-inter text-sm text-mint">{p.note || 'Backported. Start the game again.'}</Text>
        : p && p.state === 'failed' ? <Text className="font-inter text-sm text-coral">{p.note}</Text> : null}
      <View className="flex-row pt-[4px]">
        <Button small primary={needs} label={b ? 'Backporting…' : 'Backport on this PS5'} disabled={b}
          onPress={() => ask({title: `Backport ${game.name}?`, yes: 'Backport it',
            body: 'Your PC changes its programs and settings on this PS5 so it starts on this firmware. The originals are kept on the PC. Close the game first if it is running.',
            onYes: start})} />
      </View>
    </View>
  );
}

// ---------- the app ----------

function App() {
  const [phase, setPhase] = useState({kind: 'starting', text: 'Finding your PC…'});
  const [games, setGames] = useState([]);
  const [status, setStatus] = useState(null);
  const [open, setOpen] = useState(null);
  const [tab, setTab] = useState('games');
  const [rom, setRom] = useState(null);
  const [drive, setDrive] = useState('');
  const [message, setMessage] = useState(null);
  const [romNote, setRomNote] = useState(null);
  const [pcBuild, setPcBuild] = useState('');
  const [updating, setUpdating] = useState(null);
  const gamesAt = useRef(0);
  const wasBusy = useRef(new Set());
  const working = useRef(false); // the PC is building or sending: poll faster

  const start = useCallback(async () => {
    setPhase({kind: 'starting', text: 'Finding your PC…'});
    if (!loadConfig()) {
      setPhase({kind: 'setup'});
      return;
    }
    try {
      const hello = await connect();
      setPcBuild(hello.appBuild || '');
      setPhase({kind: 'starting', text: `Loading your games from ${hello.pc || 'your PC'}…`});
      const [g, s] = await Promise.all([api.games(), api.status()]);
      setGames((g.games || []).sort(byName));
      setStatus(s);
      setDrive(d => d || s.drive || '');
      gamesAt.current = Date.now();
      setPhase({kind: 'ready'});
    } catch (err) {
      setPhase({kind: 'error', text: err.message});
    }
  }, []);

  useEffect(() => { start(); }, [start]);

  // keep the progress fresh; reload the full list now and then or when a download finishes
  useEffect(() => {
    if (phase.kind !== 'ready') return undefined;
    let stop = false, timer = 0;
    const tick = async () => {
      try {
        const s = await api.status();
        if (stop) return;
        setStatus(s);
        working.current = s.build.busy || s.ps5.busy;
        const now = new Set(Object.entries(s.games || {}).filter(([, p]) => busy(p)).map(([k]) => k));
        const finished = [...wasBusy.current].some(k => !now.has(k));
        wasBusy.current = now;
        if (finished || Date.now() - gamesAt.current > GAMES_EVERY) {
          const g = await api.games();
          if (!stop) { setGames((g.games || []).sort(byName)); gamesAt.current = Date.now(); }
        }
      } catch (err) {
        if (err.unpaired) { setPhase({kind: 'error', text: err.message}); return; }
        // a missed poll is fine; the next one tries again
      }
      if (!stop) timer = setTimeout(tick, working.current ? POLL_BUSY : POLL_IDLE);
    };
    timer = setTimeout(tick, POLL_IDLE);
    return () => { stop = true; clearTimeout(timer); };
  }, [phase.kind]);

  const progress = useMemo(() => {
    const p = {};
    for (const g of games) if (g.state) p[g.key] = {state: g.state, pct: g.pct, note: g.note};
    Object.assign(p, (status && status.games) || {});
    return p;
  }, [games, status]);

  const openGame = useCallback(key => { setMessage(null); setOpen(key); }, []);
  const back = useCallback(() => setOpen(null), []);
  const pickRom = useCallback(r => { setRomNote(null); setRom(r); }, []);

  // the PC has a newer build of this app. Loadout puts it on the PS5 beside
  // this one; when it's there, this app closes and Loadout swaps it in.
  // Nothing in the app's folder changes while it's open or suspended, which
  // is what made the PS5 refuse to start it.
  const update = !!pcBuild && !!LAUNCHED && pcBuild !== LAUNCHED;
  const startUpdate = useCallback(async () => {
    setUpdating({text: 'Getting the update from your PC…'});
    try {
      let state = '';
      for (let i = 0; i < 400; i++) {
        const r = await api.appUpdate();
        state = (r.data && r.data.state) || '';
        if (state === 'staged' || state === 'current') break;
        if (state !== 'queued') throw new Error("Your PC couldn't send the update. Check that Loadout can reach the PS5, then try again.");
        const s = await api.status().catch(() => null);
        const p = s && s.games && s.games['app:update'];
        if (p && p.state === 'failed') throw new Error(`The update didn't arrive: ${p.note}`);
        setUpdating({text: `Getting the update${p && p.pct ? ` ${Math.floor(p.pct / 10)}%` : '…'}`});
        await sleep(1500);
      }
      if (state === 'queued') throw new Error('The update is taking too long. Try again in a moment.');
      if (state === 'staged') await api.appSwap();
      setUpdating({text: 'Loadout will close to finish updating. Open it again in about half a minute.'});
      await sleep(4000);
      BackHandler.exitApp();
      // only seen if the PS5 didn't close it
      setUpdating({text: 'Close Loadout (Options, then Close Application), wait half a minute and open it again.', done: true});
    } catch (err) {
      setUpdating({text: err.message, bad: true});
    }
  }, [pcBuild]);

  // DLC installs run on the PC; this remembers which job is each DLC's latest
  const [installs, setInstalls] = useState({});
  const startDLC = useCallback(async (game, d) => {
    const r = await api.dlcInstall(game.key, d.key);
    const data = r.data || {};
    if (data.job) setInstalls(m => ({...m, [d.key]: data.job}));
    refresh();
    return data.msg || 'Started.';
  }, []);
  // the note about a ROM download fades after a while
  useEffect(() => {
    if (!romNote || rom) return undefined;
    const t = setTimeout(() => setRomNote(null), 8000);
    return () => clearTimeout(t);
  }, [romNote, rom]);

  const refresh = async () => {
    try {
      const s = await api.status();
      setStatus(s);
      working.current = s.build.busy || s.ps5.busy;
    } catch (err) {
      // the next poll catches up
    }
  };

  const download = useCallback(async () => {
    const g = games.find(x => x.key === open);
    if (!g) return;
    setMessage({text: 'Asking your PC…'});
    try {
      const d = await api.download(g.key, drive);
      setMessage({text: (d.data && d.data.msg) || 'Download started.'});
      refresh();
    } catch (err) {
      setMessage({text: err.message, bad: true});
    }
  }, [games, open, drive]);

  const getRom = async r => {
    setRomNote({text: `Asking your PC to send ${r.name}…`});
    try {
      const d = await api.romGet(r.key);
      setRomNote({text: (d.data && d.data.msg) || 'Downloading.'});
      refresh();
    } catch (err) {
      setRomNote({text: err.message, bad: true});
    }
  };

  if (phase.kind === 'starting') return <Message title="Welcome back" body={phase.text} />;
  if (phase.kind === 'setup') {
    return <Message title="Set up Loadout"
      body={'This app needs to be installed from Loadout on your PC so it knows where your PC is.\n\nOn the PC open Loadout › Settings › Phone remote and choose "Install the PS5 app".'}
      action="Try again" onAction={start} />;
  }
  if (phase.kind === 'error') return <Message title="Can't reach Loadout" body={phase.text} action="Try again" onAction={start} />;

  // the home screen stays mounted under the game page, so closing the page
  // puts focus back on the tile it was opened from
  const game = open && games.find(g => g.key === open);
  return (
    <View className="w-screen h-screen bg-ink">
      <FocusScope inert={!!game || !!rom}>
        <Home games={games} status={status} progress={progress} onOpen={openGame} onPickRom={pickRom} tab={tab} setTab={setTab} onChanged={refresh} drive={drive} setDrive={setDrive}
          update={update && !updating} onUpdate={startUpdate} />
      </FocusScope>
      {game ? (
        <View className="absolute inset-0">
          <Detail game={game} status={status} progress={progress} installs={installs} startDLC={startDLC} drive={drive} setDrive={setDrive}
            onBack={back} onDownload={download} message={message} />
        </View>
      ) : null}
      {rom ? (
        <Confirm title={`Download ${rom.name}?`} yes="Download"
          body={`It goes to ${rom.emu} on this PS5 (${rom.system}, ${size(rom.size) || 'folder'}).${romNote ? `\n\n${romNote.text}` : ''}`}
          onYes={() => getRom(rom)} onClose={() => setRom(null)} />
      ) : null}
      {updating ? (
        <FocusScope autoFocus trap onBack={() => { if (updating.bad || updating.done) setUpdating(null); return true; }}>
          <View focusable className="absolute inset-0 w-screen h-screen bg-black/80 items-center justify-center gap-[18px]">
            <Logo big />
            <Text className={`${T} text-xl font-bold text-center w-[900px]`} style={updating.bad ? {color: C.coral} : undefined}>{updating.text}</Text>
            {updating.bad || updating.done ? <Text className="font-inter text-sm text-muted">Circle to go back</Text> : null}
          </View>
        </FocusScope>
      ) : null}
      {romNote && !rom ? (
        <View className="absolute bottom-[28px] left-[48px] px-[18px] py-[10px] rounded-xl bg-surface border border-line">
          <Text className="font-inter text-sm" style={{color: romNote.bad ? C.coral : C.sky}}>{romNote.text}</Text>
        </View>
      ) : null}
    </View>
  );
}

AppRegistry.registerComponent('loadout', () => App);
