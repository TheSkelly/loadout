// Loadout for PS5: browse the games in Loadout on your PC and download them
// to this console. The PC does the work (it builds the image if needed and
// sends it); this app picks the game and shows the progress. It also installs
// DLC packages, backs up and restores saves, and downloads games from the
// PC's ROM library to the emulators on this PS5.
//
// Built with PS5 React (GPL-3.0-or-later); see NOTICE in the app folder.
import {memo, useCallback, useEffect, useMemo, useRef, useState} from 'react';
import {AppRegistry, View, Text, Image, ScrollView, VirtualList, FocusScope, useIsFocused, useController, FileSystem, BackHandler} from '@ps5-react/core';
import {loadConfig, connect, api, art} from './api.js';
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

function Badge({game, prog}) {
  let label = '', color = C.mint;
  if (busy(prog)) { label = WORD[prog.state] || prog.state; color = C.accent; }
  else if (prog && prog.state === 'failed') { label = 'Failed'; color = C.coral; }
  else if (game.onPs5) { label = 'Installed'; color = C.sky; }
  if (!label) return null;
  return (
    <View className="absolute left-[10px] top-[10px] px-[10px] py-[3px] rounded-full bg-ink/90">
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
      <Badge game={game} prog={prog} />
      {busy(prog) && prog.pct > 0 ? <View className="absolute left-[10px] bottom-[10px]"><Bar pct={prog.pct / 10} width={150} /></View> : null}
    </View>
  );
}

function TileName({name}) {
  const focused = useIsFocused();
  return <Text focused={focused} className="font-inter text-sm text-muted focused:text-text truncate w-[176px]">{name}</Text>;
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

function Heading({title, note, pad = 48}) {
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
    <View scrollAnchor className="gap-[4px] pt-[16px]">
      <Heading title={title} note={note} />
      <ScrollView horizontal className="w-screen h-[235px] flex-row items-start gap-[22px] py-[12px]">
        <View className="w-[26px] h-[1px]" />
        {games.map((g, i) => <Tile key={g.key} game={g} prog={progress[g.key]} onOpen={onOpen} onSpot={onSpot} end={i === last ? `end:${id}` : undefined} />)}
        <View className="w-[26px] h-[1px]" />
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

function Button({label, onPress, primary, disabled, small}) {
  if (disabled) {
    return (
      <View focusable className={small ? 'px-[20px] py-[7px] rounded-lg bg-raised border-[3px] border-transparent focused:border-text'
        : 'px-[34px] py-[13px] rounded-xl bg-raised border-[3px] border-transparent focused:border-text'}>
        <Text className={small ? 'font-inter text-sm text-muted' : 'font-inter text-base text-muted'}>{label}</Text>
      </View>
    );
  }
  if (small) {
    return primary ? (
      <View onPress={onPress} className="px-[20px] py-[7px] rounded-lg bg-accent border-[3px] border-transparent focused:border-text">
        <Text className="font-inter text-sm font-bold text-ink">{label}</Text>
      </View>
    ) : (
      <View onPress={onPress} className="px-[20px] py-[7px] rounded-lg bg-raised border-[3px] border-transparent focused:border-accent">
        <Text className="font-inter text-sm text-text">{label}</Text>
      </View>
    );
  }
  return primary ? (
    <View onPress={onPress} className="px-[40px] py-[13px] rounded-xl bg-accent border-[3px] border-transparent transition focused:border-text focused:scale-105">
      <Text className="font-inter text-base font-bold text-ink">{label}</Text>
    </View>
  ) : (
    <View onPress={onPress} className="px-[34px] py-[13px] rounded-xl bg-raised/90 border-[3px] border-transparent transition focused:border-accent focused:scale-105">
      <Text className="font-inter text-base text-text">{label}</Text>
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

// the top of the home screen: the focused game's art and what it is
function Backdrop({hero}) {
  const [shown, setShown] = useState(hero);
  useEffect(() => {
    const t = setTimeout(() => setShown(hero), BACKDROP_WAIT);
    return () => clearTimeout(t);
  }, [hero]);
  const uri = art(shown, 'hero');
  return (
    <View className="absolute left-0 top-0 w-screen h-[330px] overflow-hidden">
      <View className="absolute inset-0 bg-gradient-to-br from-ink2 via-ink to-black" />
      {uri ? <Image key={uri} source={{uri}} resizeMode="cover" className="absolute right-0 top-0 w-[900px] h-[330px]" /> : null}
      <View className="absolute left-0 top-0 w-[900px] h-[330px] bg-gradient-to-r from-ink via-ink/85 to-transparent" />
      <View className="absolute left-0 bottom-0 w-screen h-[120px] bg-gradient-to-t from-ink to-transparent" />
    </View>
  );
}

function Spotlight({game, prog}) {
  if (!game) return null;
  const st = stateOf(game, prog);
  const meta = [game.platform, size(game.size), (game.genres || []).slice(0, 2).join(', '), game.rating ? `★ ${game.rating.toFixed(1)}` : '',
    game.dlc && game.dlc.length ? `${game.dlc.length} DLC` : ''].filter(Boolean).join('   ·   ');
  return (
    <View className="w-[680px] px-[48px] pt-[12px] gap-[8px]">
      <Text className={`${T} text-[38px] leading-[46px] font-bold line-clamp-2`}>{game.name}</Text>
      <Text className="font-inter text-sm text-muted line-clamp-1">{meta}</Text>
      {st ? <Text className="font-inter text-sm" style={{color: st.color}}>{st.label}</Text> : null}
      {game.desc ? <Text className="font-inter text-sm text-text/80 line-clamp-2 pt-[2px]">{game.desc}</Text> : null}
    </View>
  );
}

function Tab({label, on, onPress}) {
  return on ? (
    <View onPress={onPress} className="px-[18px] py-[6px] rounded-full bg-accent border-2 border-transparent focused:border-text">
      <Text className="font-inter text-sm font-bold text-ink">{label}</Text>
    </View>
  ) : (
    <View onPress={onPress} className="px-[18px] py-[6px] rounded-full bg-surface/80 border-2 border-transparent focused:border-accent">
      <Text className="font-inter text-sm text-text">{label}</Text>
    </View>
  );
}

function TopBar({tab, setTab, status, update}) {
  return (
    <View className="flex-row items-center px-[48px] pt-[22px] gap-[16px]">
      <Logo />
      <FocusScope>
        <View className="flex-row gap-[10px] pl-[18px]">
          <Tab label="Games" on={tab === 'games'} onPress={() => setTab('games')} />
          <Tab label="Emulators" on={tab === 'emu'} onPress={() => setTab('emu')} />
        </View>
      </FocusScope>
      <Text className="font-inter text-xs text-faint">L1 / R1</Text>
      <View className="flex-1" />
      {update ? (
        <View className="flex-row items-center gap-[8px] px-[14px] py-[6px] rounded-full bg-accent">
          <Text className="font-inter text-sm font-bold text-ink">Update ready</Text>
          <Text className="font-inter text-xs text-ink">Press Triangle</Text>
        </View>
      ) : null}
      <Activity status={status} />
      <View className="items-end">
        {status && status.pc ? <Text className="font-inter text-sm text-muted">{status.pc}</Text> : null}
        {LAUNCHED ? <Text className="font-inter text-xs text-faint">{`Build ${LAUNCHED.slice(0, 7)}`}</Text> : null}
      </View>
    </View>
  );
}

function Games({games, progress, onOpen}) {
  const [spot, setSpot] = useState(null);
  const shelves = useMemo(() => {
    const moving = games.filter(g => busy(progress[g.key]));
    const recent = games.filter(g => seen(g.firstSeen)).sort((a, b) => seen(b.firstSeen) - seen(a.firstSeen) || byName(a, b)).slice(0, 15);
    const notOn = games.filter(g => !g.onPs5 && g.canGet);
    const onPs5 = games.filter(g => g.onPs5);
    const dlc = games.filter(g => g.dlc && g.dlc.some(d => d.pkg));
    // the genres with the most games get rails of their own
    const count = {};
    for (const g of games) for (const x of g.genres || []) count[x] = (count[x] || 0) + 1;
    const genres = Object.keys(count).filter(x => count[x] >= 4).sort((a, b) => count[b] - count[a]).slice(0, 3)
      .map(x => ({name: x, games: games.filter(g => (g.genres || []).includes(x))}));
    return {moving, recent, notOn, onPs5, dlc, genres};
  }, [games, progress]);
  const onSpot = useCallback(key => { setSpot(key); tellSpot(key); }, []);
  const renderItem = useCallback(({item}) => <Tile game={item} prog={progress[item.key]} onOpen={onOpen} onSpot={onSpot} />, [progress, onOpen, onSpot]);
  const game = games.find(g => g.key === spot) || shelves.recent[0] || games[0];
  return (
    <View className="flex-1">
      <View className="h-[258px]">
        <Spotlight game={game} prog={game && progress[game.key]} />
      </View>
      <FocusScope autoFocus>
        <ScrollView className="flex-1 bg-ink">
          <Shelf id="moving" title="Downloading" games={shelves.moving} progress={progress} onOpen={onOpen} onSpot={onSpot} />
          <Shelf id="recent" title="New in your library" games={shelves.recent} progress={progress} onOpen={onOpen} onSpot={onSpot} />
          <Shelf id="noton" title="Ready for your PS5" note={`${shelves.notOn.length} games`} games={shelves.notOn} progress={progress} onOpen={onOpen} onSpot={onSpot} />
          <Shelf id="dlc" title="DLC to install" note="open a game to install its DLC" games={shelves.dlc} progress={progress} onOpen={onOpen} onSpot={onSpot} />
          {shelves.genres.map(x => <Shelf key={x.name} id={`g:${x.name}`} title={x.name} games={x.games} progress={progress} onOpen={onOpen} onSpot={onSpot} />)}
          <Shelf id="onps5" title="On your PS5" games={shelves.onPs5} progress={progress} onOpen={onOpen} onSpot={onSpot} />
          <View scrollAnchor className="gap-[14px] pt-[16px] pb-[48px]">
            <Heading title="All games" note={`${games.length}, A to Z`} />
            <View className="px-[48px] pt-[6px]">
              <VirtualList data={games} keyExtractor={g => g.key} renderItem={renderItem}
                numColumns={6} itemHeight={TILE_H} rowGap={26} columnGap={25} />
            </View>
          </View>
          {!games.length ? <Text className="font-inter text-base text-muted px-[48px] pt-[30px]">No games in Loadout yet. Pick your games folder in Loadout on the PC.</Text> : null}
        </ScrollView>
      </FocusScope>
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
      <View className="h-[258px] px-[48px] pt-[12px] gap-[8px] w-[760px]">
        {rom ? <Text className={`${T} text-[38px] leading-[46px] font-bold line-clamp-2`}>{rom.name}</Text> : null}
        {rom ? <Text className="font-inter text-sm text-muted">{[rom.system, size(rom.size), rom.file].filter(Boolean).join('   ·   ')}</Text> : null}
        {rom ? <Text className="font-inter text-sm text-accent">{busy(prog) ? `${WORD[prog.state] || 'Downloading'}${prog.pct ? ` · ${Math.floor(prog.pct / 10)}%` : ''}` : prog && prog.state === 'sent' ? `Downloaded to ${rom.emu}` : `Downloads to ${rom.emu} on this PS5`}</Text> : null}
        {!rom ? <Text className={`${T} text-[38px] leading-[46px] font-bold`}>Emulators</Text> : null}
        {!rom ? <Text className="font-inter text-sm text-muted">Games from the ROM library on your PC, sent straight to the right emulator.</Text> : null}
      </View>
      {empty ? (
        <FocusScope autoFocus>
          <View className="px-[48px] pt-[10px] w-[900px] gap-[16px]">
            <Text className="font-inter text-base text-muted">{empty}</Text>
            <View focusable className="w-[1px] h-[1px]" />
          </View>
        </FocusScope>
      ) : (
        <FocusScope autoFocus>
          <ScrollView className="flex-1 bg-ink">
            {systems.map(s => {
              const last = s.games.length - 1;
              return (
                <View key={s.name} scrollAnchor className="gap-[4px] pt-[16px]">
                  <Heading title={s.name} note={`${s.games.length} · to ${s.emu}`} />
                  <ScrollView horizontal className="w-screen h-[235px] flex-row items-start gap-[22px] py-[12px]">
                    <View className="w-[26px] h-[1px]" />
                    {s.games.map((r, i) => <RomTile key={r.key} rom={r} prog={progress[`rom:${r.key}`]} onPick={onPick} onSpot={onSpot} end={i === last ? `end:rom:${s.name}` : undefined} />)}
                    <View className="w-[26px] h-[1px]" />
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

function Home({games, status, progress, onOpen, onPickRom, tab, setTab, update, onUpdate}) {
  const flip = useCallback(action => {
    if (action === 'triangle' && update) {
      onUpdate();
      return true;
    }
    if (action !== 'l1' && action !== 'r1') return false;
    setTab(t => t === 'games' ? 'emu' : 'games');
    return true;
  }, [setTab, update, onUpdate]);
  return (
    <FocusScope onAction={flip}>
      <View className="w-screen h-screen bg-ink">
        {tab === 'games' ? <BackdropFor games={games} /> : <View className="absolute left-0 top-0 w-screen h-[330px] bg-gradient-to-br from-ink2 via-ink to-black" />}
        <TopBar tab={tab} setTab={setTab} status={status} update={update} />
        {tab === 'games'
          ? <Games key="games" games={games} progress={progress} onOpen={onOpen} />
          : <Emulators key="emu" progress={progress} onPick={onPickRom} />}
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
function RowText({title, sub}) {
  return (
    <View className="flex-1 gap-[2px]">
      <Text className={`${T} text-base truncate`}>{title}</Text>
      {sub ? <Text className="font-inter text-xs text-muted truncate">{sub}</Text> : null}
    </View>
  );
}

function Row({title, sub, action, primary, onPress}) {
  return onPress ? (
    <View onPress={onPress} className="w-[1150px] flex-row items-center gap-[16px] px-[18px] py-[10px] rounded-xl bg-surface/85 border-[3px] border-line focused:border-accent">
      <RowText title={title} sub={sub} />
      {action ? (
        <View className={primary ? 'px-[18px] py-[6px] rounded-lg bg-accent' : 'px-[18px] py-[6px] rounded-lg bg-raised'}>
          <Text className={primary ? 'font-inter text-sm font-bold text-ink' : 'font-inter text-sm text-text'}>{action}</Text>
        </View>
      ) : null}
    </View>
  ) : (
    <View focusable className="w-[1150px] flex-row items-center gap-[16px] px-[18px] py-[10px] rounded-xl bg-surface/85 border-[3px] border-line focused:border-text">
      <RowText title={title} sub={sub} />
      {action ? <Text className="font-inter text-sm text-faint">{action}</Text> : null}
    </View>
  );
}

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
      <Heading title="DLC" note={`${list.length} on your PC`} pad={0} />
      {list.map(d => {
        const line = dlcLine(progress[installs[d.key]]);
        const sub = [d.kind, size(d.size), line.text, d.pkg ? '' : 'Only .pkg DLC installs from here'].filter(Boolean).join('   ·   ');
        return d.pkg && !line.busy ? (
          <Row key={d.key} title={d.name} sub={sub} action={line.done ? 'Install again' : 'Install'} primary={!line.done}
            onPress={() => ask({title: `Install ${d.name}?`, yes: 'Install', onYes: () => install(d),
              body: 'Your PC copies the package to the PS5 and installs it. You can leave this page; the progress shows here and on the PC.'})} />
        ) : (
          <Row key={d.key} title={d.name} sub={sub} action={line.busy ? 'Installing…' : 'Can’t install'} />
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
  const chips = [game.platform, game.version && `v${game.version}`, size(game.size), ...(game.genres || []).slice(0, 3), game.rated, game.id].filter(Boolean);
  const where = game.onPs5 ? `On your PS5 · ${game.onPs5}` : st ? st.label : '';
  const label = b ? `${WORD[prog.state] || prog.state}…` : game.onPs5 ? 'Download again' : 'Download';
  const shots = game.lib ? Math.min(game.shots || 0, 6) : 0;
  const shown = note || message;
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
                {game.desc ? <Text className="font-inter text-sm text-text/85 line-clamp-4 pt-[4px]">{game.desc}</Text> : null}
              </View>
            </View>
            <View className="flex-row items-center gap-[16px] pt-[6px]">
              <Button primary label={label} onPress={() => { setNote(null); onDownload(); }} disabled={b || !game.canGet} />
              <Button label="Back" onPress={onBack} />
              {where ? <Text className="font-inter text-sm pl-[8px] truncate w-[620px]" style={{color: st ? st.color : C.muted}}>{where}</Text> : null}
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

  // the PC has a newer build of this app: get it now, then restart into it
  const update = !!pcBuild && !!LAUNCHED && pcBuild !== LAUNCHED;
  const startUpdate = useCallback(async () => {
    setUpdating({text: 'Getting the update from your PC…'});
    try {
      if (readStamp() !== pcBuild) {
        const r = await api.appUpdate();
        if (r.data && r.data.state === 'no') throw new Error("Your PC couldn't send the update. Reinstall the app from Loadout's phone page or Settings.");
        for (let i = 0; i < 300 && readStamp() !== pcBuild; i++) {
          await sleep(1000);
          const s = await api.status().catch(() => null);
          const p = s && s.games && s.games['app:update'];
          if (p && p.state === 'failed') throw new Error(`The update didn't arrive: ${p.note}`);
          setUpdating({text: `Updating Loadout${p && p.pct ? ` ${Math.floor(p.pct / 10)}%` : '…'}`});
        }
        if (readStamp() !== pcBuild) throw new Error('The update is taking too long. Try again in a moment.');
      }
      setUpdating({text: 'Restarting Loadout…'});
      await sleep(300);
      if (!BackHandler.exitApp({relaunch: true})) {
        setUpdating({text: 'Updated. Close Loadout (Options › Close Application) and open it again.', done: true});
      }
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
        <Home games={games} status={status} progress={progress} onOpen={openGame} onPickRom={pickRom} tab={tab} setTab={setTab}
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
