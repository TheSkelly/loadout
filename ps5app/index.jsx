// Loadout for PS5: browse the games in Loadout on your PC and download them
// to this console. The PC does the work (it builds the image if needed and
// sends it); this app picks the game and shows the progress.
//
// Built with PS5 React (GPL-3.0-or-later); see NOTICE in the app folder.
import {memo, useCallback, useEffect, useMemo, useRef, useState} from 'react';
import {AppRegistry, View, Text, Image, ScrollView, VirtualList, FocusScope, useIsFocused, useController} from '@ps5-react/core';
import {loadConfig, connect, api, art} from './api.js';

const T = 'font-inter text-text';
const POLL_BUSY = 1500, POLL_IDLE = 5000, GAMES_EVERY = 60000;
// the backdrop follows focus once it settles, so a held key doesn't load every hero on the way
const BACKDROP_WAIT = 350;

// on the PS5 a send is a download
const WORD = {queued: 'Queued', building: 'Preparing on PC', waiting: 'Waiting to download', sending: 'Downloading', sent: 'Downloaded', failed: 'Download failed'};
const busy = p => !!p && !!p.state && p.state !== 'sent' && p.state !== 'failed';

const size = b => !(b > 0) ? '' : b >= 1e12 ? `${(b / 1e12).toFixed(2)} TB` : b >= 1e9 ? `${(b / 1e9).toFixed(1)} GB` : `${Math.round(b / 1e6)} MB`;
const speed = b => b > 0 ? `${Math.round(b / 1e6)} MB/s` : '';
const dur = s => !(s > 0) ? '' : s >= 3600 ? `${Math.floor(s / 3600)} h ${Math.round(s % 3600 / 60)} min` : s >= 60 ? `${Math.round(s / 60)} min` : `${Math.round(s)} s`;
const MONTHS = ['jan', 'feb', 'mar', 'apr', 'may', 'jun', 'jul', 'aug', 'sep', 'oct', 'nov', 'dec'];
// firstSeen comes as "Oct 1, 2026"; a number sorts it
const seen = s => {
  const m = /^([A-Za-z]{3})[a-z]* (\d+), (\d{4})/.exec(s || '');
  return m ? Number(m[3]) * 10000 + (MONTHS.indexOf(m[1].toLowerCase()) + 1) * 100 + Number(m[2]) : 0;
};
const byName = (a, b) => a.name.toLowerCase() < b.name.toLowerCase() ? -1 : 1;

// ---------- small pieces ----------

// class names are compiled at build time, so colors picked at run time go in style
const C = {manila: '#E9C46A', mint: '#5BC8A0', coral: '#EF6F6C', sky: '#59B8F0', muted: '#8C9AB3'};

// tiles are square: PS5 icons are 512 x 512
const TILE = 176, NAME_H = 21, TILE_H = TILE + 8 + NAME_H;

// width is in logical pixels: the bar's fill is sized from it
function Bar({pct, width, color = C.manila}) {
  const fill = Math.round(width * Math.max(0, Math.min(100, pct)) / 100);
  return (
    <View className="h-[6px] rounded-full bg-line overflow-hidden" style={{width}}>
      <View className="h-[6px]" style={{width: fill, backgroundColor: color}} />
    </View>
  );
}

// what a game is doing, for its badge and the spotlight
function stateOf(game, prog) {
  if (busy(prog)) return {label: WORD[prog.state] || prog.state, color: C.manila};
  if (prog && prog.state === 'failed') return {label: 'Download failed', color: C.coral};
  if (game.onPs5) return {label: 'On your PS5', color: C.sky};
  if (game.ready) return {label: 'Ready to download', color: C.mint};
  if (game.canGet) return {label: 'Your PC prepares it first', color: C.muted};
  return null;
}

function Badge({game, prog}) {
  let label = '', color = C.mint;
  if (busy(prog)) { label = WORD[prog.state] || prog.state; color = C.manila; }
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
      transition focused:border-text focused:scale-105">
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

const Tile = memo(function Tile({game, prog, onOpen, onSpot}) {
  return (
    <View onPress={() => onOpen(game.key)} onFocus={() => onSpot(game.key)} className="w-[176px] gap-[8px]">
      <TileArt game={game} prog={prog} />
      <TileName name={game.name} />
    </View>
  );
});

// a rail of tiles; the ScrollView itself is the row, so the row overflows it and scrolls
const Shelf = memo(function Shelf({title, note, games, progress, onOpen, onSpot}) {
  if (!games.length) return null;
  return (
    <View scrollAnchor className="gap-[4px] pt-[16px]">
      <View className="flex-row items-end gap-[12px] px-[48px]">
        <Text className={`${T} text-lg font-bold`}>{title}</Text>
        {note ? <Text className="font-inter text-sm text-faint">{note}</Text> : null}
      </View>
      <ScrollView horizontal className="w-screen h-[235px] flex-row items-start gap-[22px] py-[12px]">
        <View className="w-[26px] h-[1px]" />
        {games.map(g => <Tile key={g.key} game={g} prog={progress[g.key]} onOpen={onOpen} onSpot={onSpot} />)}
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
        <Text className="font-inter text-xs text-text truncate w-[260px]">{lane.what || 'Working…'}</Text>
        {sub ? <Text className="font-inter text-xs text-muted truncate w-[260px]">{sub}</Text> : null}
      </View>
      <Bar pct={lane.pct / 10} width={110} color={status.ps5.busy ? C.sky : C.manila} />
    </View>
  );
}

function Logo() {
  return (
    <View className="flex-row items-center gap-[12px]">
      <View className="w-[30px] h-[30px] rounded-lg bg-manila items-center justify-center">
        <Text className="font-inter text-base font-bold text-ink">L</Text>
      </View>
      <Text className={`${T} text-xl font-bold`}>Loadout</Text>
    </View>
  );
}

// ---------- screens ----------

function Message({title, body, action, onAction}) {
  return (
    <FocusScope autoFocus>
      <View className="w-screen h-screen bg-ink items-center justify-center gap-[16px] px-[160px]">
        <Logo />
        <Text className={`${T} text-title font-bold pt-[18px]`}>{title}</Text>
        <Text className="font-inter text-base text-muted text-center">{body}</Text>
        {action ? (
          <View onPress={onAction} className="mt-[12px] px-[28px] py-[12px] rounded-xl bg-manila border-[3px] border-transparent focused:border-text">
            <Text className="font-inter text-base font-bold text-ink">{action}</Text>
          </View>
        ) : null}
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
      {uri ? <Image key={uri} source={{uri}} resizeMode="cover" className="absolute right-0 top-0 w-[900px] h-[330px]" /> : null}
      <View className="absolute left-0 top-0 w-[900px] h-[330px] bg-gradient-to-r from-ink via-ink/85 to-transparent" />
      <View className="absolute left-0 bottom-0 w-screen h-[120px] bg-gradient-to-t from-ink to-transparent" />
    </View>
  );
}

function Spotlight({game, prog}) {
  if (!game) return null;
  const st = stateOf(game, prog);
  const meta = [game.platform, size(game.size), (game.genres || []).slice(0, 2).join(', '), game.rating ? `★ ${game.rating.toFixed(1)}` : ''].filter(Boolean).join('   ·   ');
  return (
    <View className="w-[680px] px-[48px] pt-[18px] gap-[8px]">
      <Text className={`${T} text-[38px] leading-[46px] font-bold line-clamp-2`}>{game.name}</Text>
      <Text className="font-inter text-sm text-muted line-clamp-1">{meta}</Text>
      {st ? <Text className="font-inter text-sm" style={{color: st.color}}>{st.label}</Text> : null}
      {game.desc ? <Text className="font-inter text-sm text-text/80 line-clamp-2 pt-[2px]">{game.desc}</Text> : null}
    </View>
  );
}

function Home({games, status, progress, onOpen}) {
  const [spot, setSpot] = useState(null);
  const shelves = useMemo(() => {
    const moving = games.filter(g => busy(progress[g.key]));
    const recent = games.filter(g => seen(g.firstSeen)).sort((a, b) => seen(b.firstSeen) - seen(a.firstSeen) || byName(a, b)).slice(0, 15);
    const notOn = games.filter(g => !g.onPs5 && g.canGet);
    const onPs5 = games.filter(g => g.onPs5);
    // the genres with the most games get rails of their own
    const count = {};
    for (const g of games) for (const x of g.genres || []) count[x] = (count[x] || 0) + 1;
    const genres = Object.keys(count).filter(x => count[x] >= 4).sort((a, b) => count[b] - count[a]).slice(0, 3)
      .map(x => ({name: x, games: games.filter(g => (g.genres || []).includes(x))}));
    return {moving, recent, notOn, onPs5, genres};
  }, [games, progress]);
  const onSpot = useCallback(key => setSpot(key), []);
  const renderItem = useCallback(({item}) => <Tile game={item} prog={progress[item.key]} onOpen={onOpen} onSpot={onSpot} />, [progress, onOpen, onSpot]);
  const game = games.find(g => g.key === spot) || shelves.recent[0] || games[0];
  return (
    <View className="w-screen h-screen bg-ink">
      <Backdrop hero={game && game.hero} />
      <View className="h-[330px]">
        <View className="flex-row items-center px-[48px] pt-[26px] gap-[16px]">
          <Logo />
          <View className="flex-1" />
          <Activity status={status} />
          {status && status.pc ? <Text className="font-inter text-sm text-muted">{status.pc}</Text> : null}
        </View>
        <Spotlight game={game} prog={game && progress[game.key]} />
      </View>
      <FocusScope autoFocus>
        <ScrollView className="flex-1 bg-ink">
          <Shelf title="Downloading" games={shelves.moving} progress={progress} onOpen={onOpen} onSpot={onSpot} />
          <Shelf title="New in your library" games={shelves.recent} progress={progress} onOpen={onOpen} onSpot={onSpot} />
          <Shelf title="Ready for your PS5" note={`${shelves.notOn.length} games`} games={shelves.notOn} progress={progress} onOpen={onOpen} onSpot={onSpot} />
          {shelves.genres.map(x => <Shelf key={x.name} title={x.name} games={x.games} progress={progress} onOpen={onOpen} onSpot={onSpot} />)}
          <Shelf title="On your PS5" games={shelves.onPs5} progress={progress} onOpen={onOpen} onSpot={onSpot} />
          <View scrollAnchor className="gap-[14px] pt-[16px] pb-[48px]">
            <View className="flex-row items-end gap-[12px] px-[48px]">
              <Text className={`${T} text-lg font-bold`}>All games</Text>
              <Text className="font-inter text-sm text-faint">{`${games.length}, A to Z`}</Text>
            </View>
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

function Button({label, onPress, primary, disabled}) {
  if (disabled) {
    return (
      <View focusable className="px-[34px] py-[13px] rounded-xl bg-raised border-[3px] border-transparent focused:border-text">
        <Text className="font-inter text-base text-muted">{label}</Text>
      </View>
    );
  }
  return primary ? (
    <View onPress={onPress} className="px-[40px] py-[13px] rounded-xl bg-manila border-[3px] border-transparent transition focused:border-text focused:scale-105">
      <Text className="font-inter text-base font-bold text-ink">{label}</Text>
    </View>
  ) : (
    <View onPress={onPress} className="px-[34px] py-[13px] rounded-xl bg-raised/90 border-[3px] border-transparent transition focused:border-text focused:scale-105">
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

function Shot({k, n}) {
  const focused = useIsFocused();
  return (
    <View focused={focused} className="w-[352px] h-[198px] rounded-xl bg-raised border-[3px] border-transparent overflow-hidden transition focused:border-text focused:scale-105">
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

function Detail({game, status, prog, drive, setDrive, onBack, onDownload, message}) {
  const [shot, setShot] = useState(-1);
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
                    <Text className="font-inter text-base font-bold" style={{color: C.manila}}>{`★ ${game.rating.toFixed(1)}`}</Text>
                    {game.ratings ? <Text className="font-inter text-sm text-muted">{`${game.ratings} ratings`}</Text> : null}
                  </View>
                ) : null}
                <View className="flex-row flex-wrap gap-[8px] pt-[2px]">{chips.map(c => <Chip key={c}>{c}</Chip>)}</View>
                {game.desc ? <Text className="font-inter text-sm text-text/85 line-clamp-4 pt-[4px]">{game.desc}</Text> : null}
              </View>
            </View>
            <View className="flex-row items-center gap-[16px] pt-[6px]">
              <Button primary label={label} onPress={onDownload} disabled={b || !game.canGet} />
              <Button label="Back" onPress={onBack} />
              {where ? <Text className="font-inter text-sm pl-[8px] truncate w-[620px]" style={{color: st ? st.color : C.muted}}>{where}</Text> : null}
            </View>
            {drives.length ? (
              <View className="gap-[8px]">
                <Text className="font-inter text-xs text-faint tracking-widest">DOWNLOAD TO</Text>
                <View className="flex-row gap-[12px]">
                  {drives.map(d => (
                    <View key={d.root} onPress={() => setDrive(d.root)} selected={d.root === drive}
                      className="px-[18px] py-[9px] rounded-xl bg-surface/90 border-[3px] border-line selected:border-sky focused:border-text">
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
                <Bar pct={prog.state === 'sent' ? 100 : (prog.pct || 0) / 10} width={600} color={prog.state === 'failed' ? C.coral : C.manila} />
                {prog.note ? <Text className="font-inter text-sm text-muted">{prog.note}</Text> : null}
              </View>
            ) : null}
            {message ? <Text className="font-inter text-sm" style={{color: message.bad ? C.coral : C.muted}}>{message.text}</Text> : null}
          </View>
          {shots ? (
            <View scrollAnchor className="gap-[4px] pt-[28px] pb-[40px]">
              <Text className={`${T} text-lg font-bold px-[64px]`}>Screenshots</Text>
              <ScrollView horizontal className="w-screen h-[230px] flex-row items-start gap-[20px] py-[12px]">
                <View className="w-[44px] h-[1px]" />
                {Array.from({length: shots}, (_, n) => (
                  <View key={n} onPress={() => setShot(n)}><Shot k={game.lib} n={n} /></View>
                ))}
                <View className="w-[44px] h-[1px]" />
              </ScrollView>
            </View>
          ) : null}
        </ScrollView>
        {shot >= 0 ? <Viewer k={game.lib} count={shots} start={shot} onClose={() => setShot(-1)} /> : null}
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
  const [drive, setDrive] = useState('');
  const [message, setMessage] = useState(null);
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

  const download = useCallback(async () => {
    const g = games.find(x => x.key === open);
    if (!g) return;
    setMessage({text: 'Asking your PC…'});
    try {
      const d = await api.download(g.key, drive);
      setMessage({text: (d.data && d.data.msg) || 'Download started.'});
      const s = await api.status();
      setStatus(s);
      working.current = s.build.busy || s.ps5.busy;
    } catch (err) {
      setMessage({text: err.message, bad: true});
    }
  }, [games, open, drive]);

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
      <FocusScope inert={!!game}>
        <Home games={games} status={status} progress={progress} onOpen={openGame} />
      </FocusScope>
      {game ? (
        <View className="absolute inset-0">
          <Detail game={game} status={status} prog={progress[game.key]} drive={drive} setDrive={setDrive}
            onBack={back} onDownload={download} message={message} />
        </View>
      ) : null}
    </View>
  );
}

AppRegistry.registerComponent('loadout', () => App);
