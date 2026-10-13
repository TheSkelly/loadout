// Loadout for PS5: browse the games in Loadout on your PC and download them
// to this console. The PC does the work (it builds the image if needed and
// sends it); this app picks the game and shows the progress.
//
// Built with PS5 React (GPL-3.0-or-later); see NOTICE in the app folder.
import {useCallback, useEffect, useMemo, useRef, useState} from 'react';
import {AppRegistry, View, Text, Image, ScrollView, VirtualList, FocusScope, useIsFocused} from '@ps5-react/core';
import {loadConfig, connect, api, art} from './api.js';

const T = 'font-inter text-text';
const POLL_BUSY = 1500, POLL_IDLE = 5000, GAMES_EVERY = 60000;

// on the PS5 a send is a download
const WORD = {queued: 'Queued', building: 'Preparing on PC', waiting: 'Waiting to download', sending: 'Downloading', sent: 'Downloaded', failed: 'Download failed'};
const busy = p => !!p && !!p.state && p.state !== 'sent' && p.state !== 'failed';

const size = b => !(b > 0) ? '' : b >= 1e12 ? `${(b / 1e12).toFixed(2)} TB` : b >= 1e9 ? `${(b / 1e9).toFixed(1)} GB` : `${Math.round(b / 1e6)} MB`;
const speed = b => b > 0 ? `${Math.round(b / 1e6)} MB/s` : '';
const dur = s => !(s > 0) ? '' : s >= 3600 ? `${Math.floor(s / 3600)} h ${Math.round(s % 3600 / 60)} min` : s >= 60 ? `${Math.round(s / 60)} min` : `${Math.round(s)} s`;

// ---------- small pieces ----------

// class names are compiled at build time, so colors picked at run time go in style
const C = {manila: '#E9C46A', mint: '#5BC8A0', coral: '#EF6F6C', sky: '#59B8F0', muted: '#8C9AB3'};

// width is in logical pixels: the bar's fill is sized from it
function Bar({pct, width, color = C.manila}) {
  const fill = Math.round(width * Math.max(0, Math.min(100, pct)) / 100);
  return (
    <View className="h-[6px] rounded-full bg-line overflow-hidden" style={{width}}>
      <View className="h-[6px]" style={{width: fill, backgroundColor: color}} />
    </View>
  );
}

function Badge({game, prog}) {
  let label = '', color = C.mint;
  if (busy(prog)) { label = WORD[prog.state] || prog.state; color = C.manila; }
  else if (prog && prog.state === 'failed') { label = 'Failed'; color = C.coral; }
  else if (game.onPs5) { label = 'Installed'; color = C.sky; }
  else if (game.ready) label = 'Ready';
  if (!label) return null;
  return (
    <View className="absolute left-[8px] top-[8px] px-[8px] py-[2px] rounded-full bg-ink/90">
      <Text className="font-inter text-xs" style={{color}}>{label}</Text>
    </View>
  );
}

// the cover reads the tile's focus to draw its highlight
function TileArt({game, prog}) {
  const focused = useIsFocused();
  const cover = art(game.cover, 'cover');
  return (
    <View focused={focused} className="w-[150px] h-[150px] rounded-2xl bg-raised border-[3px] border-transparent overflow-hidden
      transition focused:border-manila focused:scale-105">
      {cover
        ? <Image source={{uri: cover}} resizeMode="cover" className="w-[144px] h-[144px] rounded-xl bg-raised" />
        : <View className="w-[144px] h-[144px] items-center justify-center p-[10px]"><Text className={`${T} text-sm text-muted line-clamp-4`}>{game.name}</Text></View>}
      <Badge game={game} prog={prog} />
      {busy(prog) && prog.pct > 0 ? <View className="absolute left-[8px] bottom-[8px]"><Bar pct={prog.pct / 10} width={128} /></View> : null}
    </View>
  );
}

function Tile({game, prog, onOpen}) {
  return (
    <View onPress={() => onOpen(game.key)} className="w-[150px] gap-[6px]">
      <TileArt game={game} prog={prog} />
      <Text className={`${T} text-sm truncate`}>{game.name}</Text>
      <Text className="font-inter text-xs text-muted truncate">{[game.platform, size(game.size)].filter(Boolean).join(' · ')}</Text>
    </View>
  );
}

function Heading({title, note}) {
  return (
    <View className="flex-row items-end gap-[12px] px-[48px]">
      <Text className={`${T} text-lg`}>{title}</Text>
      {note ? <Text className="font-inter text-sm text-faint">{note}</Text> : null}
    </View>
  );
}

function Shelf({title, note, games, progress, onOpen}) {
  if (!games.length) return null;
  return (
    <View scrollAnchor className="gap-[10px] pt-[18px]">
      <Heading title={title} note={note} />
      <ScrollView horizontal className="h-[212px]">
        <View className="flex-row gap-[18px] px-[48px] py-[6px]">
          {games.map(g => <Tile key={g.key} game={g} prog={progress[g.key]} onOpen={onOpen} />)}
        </View>
      </ScrollView>
    </View>
  );
}

function Lane({lane, idle, color}) {
  const sub = [lane.busy && lane.total ? `${lane.done} of ${lane.total}` : '', lane.busy ? speed(lane.speed) : '', lane.busy && lane.eta ? `${dur(lane.eta)} left` : ''].filter(Boolean).join(' · ');
  return (
    <View className="flex-1 gap-[4px]">
      <Text className={`${T} text-sm truncate`}>{lane.busy && lane.what ? lane.what : idle}</Text>
      <Bar pct={lane.busy ? lane.pct / 10 : 0} width={560} color={color} />
      <Text className="font-inter text-xs text-muted">{sub || ' '}</Text>
    </View>
  );
}

// ---------- screens ----------

function Message({title, body, action, onAction}) {
  return (
    <FocusScope autoFocus>
      <View className="w-screen h-screen bg-ink items-center justify-center gap-[16px] px-[160px]">
        <Text className={`${T} text-title`}>{title}</Text>
        <Text className="font-inter text-base text-muted text-center">{body}</Text>
        {action ? (
          <View onPress={onAction} className="mt-[12px] px-[28px] py-[12px] rounded-xl bg-manila border-[3px] border-transparent focused:border-text">
            <Text className="font-inter text-base text-ink">{action}</Text>
          </View>
        ) : null}
      </View>
    </FocusScope>
  );
}

function Home({games, status, progress, onOpen}) {
  const shelves = useMemo(() => {
    const moving = games.filter(g => busy(progress[g.key]));
    const recent = games.filter(g => g.firstSeen).sort((a, b) => b.firstSeen.localeCompare(a.firstSeen)).slice(0, 15);
    const notOn = games.filter(g => !g.onPs5 && g.canGet);
    return {moving, recent, notOn: notOn.slice(0, 30), notOnCount: notOn.length};
  }, [games, progress]);
  const renderItem = useCallback(({item}) => <Tile game={item} prog={progress[item.key]} onOpen={onOpen} />, [progress, onOpen]);
  const on = status && (status.build.busy || status.ps5.busy);
  return (
    <View className="w-screen h-screen bg-ink">
      <View className="flex-row items-center px-[48px] pt-[26px] pb-[6px] gap-[12px]">
        <View className="w-[30px] h-[30px] rounded-lg bg-manila" />
        <Text className={`${T} text-xl`}>Loadout</Text>
        <View className="flex-1" />
        {status ? <Text className="font-inter text-sm text-muted">{status.pc ? `PC: ${status.pc}` : ''}</Text> : null}
      </View>
      {on ? (
        <View className="mx-[48px] mt-[8px] p-[14px] rounded-xl bg-surface border border-line flex-row gap-[24px]">
          <Lane lane={status.build} idle="Nothing to prepare" color={C.manila} />
          <Lane lane={status.ps5} idle="No downloads right now" color={C.sky} />
        </View>
      ) : null}
      <FocusScope autoFocus>
        <ScrollView className="flex-1">
          <Shelf title="Downloads" games={shelves.moving} progress={progress} onOpen={onOpen} />
          <Shelf title="Recently added" games={shelves.recent} progress={progress} onOpen={onOpen} />
          <Shelf title="Not on your PS5 yet" note={`${shelves.notOnCount} games`} games={shelves.notOn} progress={progress} onOpen={onOpen} />
          <View scrollAnchor className="gap-[10px] pt-[18px] pb-[40px]">
            <Heading title="All games" note={String(games.length)} />
            <View className="px-[48px]">
              <VirtualList data={games} keyExtractor={g => g.key} renderItem={renderItem}
                numColumns={6} itemHeight={206} rowGap={14} columnGap={46} />
            </View>
          </View>
          {!games.length ? <Text className="font-inter text-base text-muted px-[48px] pt-[30px]">No games in Loadout yet. Pick your games folder in Loadout on the PC.</Text> : null}
        </ScrollView>
      </FocusScope>
    </View>
  );
}

function Detail({game, status, prog, drive, setDrive, onBack, onDownload, message}) {
  const hero = art(game.hero, 'hero');
  const cover = art(game.cover, 'full');
  const drives = (status && status.drives) || [];
  const b = busy(prog);
  const meta = [game.platform, game.version && `v${game.version}`, size(game.size), game.publisher, game.release,
    (game.genres || []).slice(0, 3).join(', '), game.rating ? `★ ${game.rating.toFixed(1)}` : '', game.id].filter(Boolean).join('   ·   ');
  const where = game.onPs5 ? `On your PS5: ${game.onPs5}` : game.ready ? 'Ready to download.' : 'Your PC will prepare it first, then it downloads.';
  const label = b ? `${WORD[prog.state] || prog.state}…` : game.onPs5 ? 'Download again' : 'Download';
  return (
    <FocusScope autoFocus trap onBack={() => { onBack(); return true; }}>
      <View className="w-screen h-screen bg-ink">
        {hero ? <Image source={{uri: hero}} resizeMode="cover" className="absolute inset-0 w-screen h-screen" /> : null}
        <View className="absolute inset-0 bg-ink/85" />
        <View className="flex-1 px-[64px] pt-[60px] gap-[16px]">
          <View className="flex-row gap-[28px] items-end">
            {cover ? <Image source={{uri: cover}} resizeMode="cover" className="w-[180px] h-[180px] rounded-2xl bg-raised" /> : <View className="w-[180px] h-[180px] rounded-2xl bg-raised" />}
            <View className="flex-1 gap-[8px]">
              <Text className={`${T} text-title line-clamp-2`}>{game.name}</Text>
              <Text className="font-inter text-sm text-muted line-clamp-2">{meta}</Text>
            </View>
          </View>
          <Text className="font-inter text-sm text-sky">{where}</Text>
          {game.desc ? <Text className="font-inter text-base text-text line-clamp-4 w-[820px]">{game.desc}</Text> : null}
          {drives.length ? (
            <View className="gap-[6px]">
              <Text className="font-inter text-xs text-faint">DOWNLOAD TO</Text>
              <View className="flex-row gap-[12px]">
                {drives.map(d => (
                  <View key={d.root} onPress={() => setDrive(d.root)} selected={d.root === drive}
                    className="px-[16px] py-[8px] rounded-xl bg-surface border-[3px] border-line selected:border-sky focused:border-text">
                    <Text className="font-inter text-sm text-text">{d.short || d.label}</Text>
                    <Text className="font-inter text-xs text-muted">{d.free >= 0 ? `${size(d.free)} free` : d.label}</Text>
                  </View>
                ))}
              </View>
            </View>
          ) : null}
          <View className="flex-row gap-[14px] pt-[4px]">
            {b || !game.canGet ? (
              <View focusable className="px-[30px] py-[12px] rounded-xl bg-raised border-[3px] border-transparent focused:border-text">
                <Text className="font-inter text-base text-muted">{label}</Text>
              </View>
            ) : (
              <View onPress={onDownload} className="px-[30px] py-[12px] rounded-xl bg-manila border-[3px] border-transparent focused:border-text">
                <Text className="font-inter text-base text-ink">{label}</Text>
              </View>
            )}
            <View onPress={onBack} className="px-[30px] py-[12px] rounded-xl bg-raised border-[3px] border-transparent focused:border-text">
              <Text className="font-inter text-base text-text">Back</Text>
            </View>
          </View>
          {b || (prog && prog.state) ? (
            <View className="w-[560px] gap-[6px]">
              <Text className={`${T} text-base`}>{WORD[prog.state] || ''}</Text>
              <Bar pct={prog.state === 'sent' ? 100 : (prog.pct || 0) / 10} width={560} color={prog.state === 'failed' ? C.coral : C.manila} />
              {prog.note ? <Text className="font-inter text-sm text-muted">{prog.note}</Text> : null}
            </View>
          ) : null}
          {message ? <Text className="font-inter text-sm" style={{color: message.bad ? C.coral : C.muted}}>{message.text}</Text> : null}
        </View>
      </View>
    </FocusScope>
  );
}

// ---------- the app ----------

function App() {
  const [phase, setPhase] = useState({kind: 'starting'});
  const [games, setGames] = useState([]);
  const [status, setStatus] = useState(null);
  const [open, setOpen] = useState(null);
  const [drive, setDrive] = useState('');
  const [message, setMessage] = useState(null);
  const gamesAt = useRef(0);
  const wasBusy = useRef(new Set());
  const working = useRef(false); // the PC is building or sending: poll faster

  const start = useCallback(async () => {
    setPhase({kind: 'starting'});
    if (!loadConfig()) {
      setPhase({kind: 'setup'});
      return;
    }
    try {
      await connect();
      const [g, s] = await Promise.all([api.games(), api.status()]);
      setGames(g.games || []);
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
          if (!stop) { setGames(g.games || []); gamesAt.current = Date.now(); }
        }
      } catch (err) {
        if (err.unpaired) { setPhase({kind: 'error', text: err.message}); return; }
        // a missed poll is fine; the next one tries again
      }
      if (!stop) timer = setTimeout(tick, working.current ? POLL_BUSY : POLL_IDLE);
    };
    timer = setTimeout(tick, POLL_BUSY);
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

  if (phase.kind === 'starting') return <Message title="Loadout" body="Connecting to your PC…" />;
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
