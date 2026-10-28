import {parseCookieFile} from '/cookies.mjs';
import {icon} from '/icons.mjs';

const $ = (id) => document.getElementById(id);
let state = null;
let activeView = 'queue';
let pending = 0;
let queueSignature = '';
let refreshTimer;
let noticeTimer;
let loadedLiked = false;
let loadedPlaylists = false;
let importCredentials=null;
let discoveredAccounts=[];
let accountKey="";
let previousAuth=null;
const titles = {queue:'Очередь',search:'Поиск',liked:'Мои лайки',playlists:'Плейлисты',commands:'Голосовые команды',settings:'Настройки'};
const statusText = {playing:'Сейчас играет',paused:'На паузе',stopped:'Готова слушать',searching:'Ищем музыку…',buffering:'Подготовка трека…',error:'Не удалось включить трек'};
const formatTime = (ms) => { const s=Math.max(0,Math.floor((ms||0)/1000)); return `${Math.floor(s/60)}:${String(s%60).padStart(2,'0')}`; };

function notice(message, success=false) {
  clearTimeout(noticeTimer);
  $('notice').textContent=message;
  $('notice').classList.toggle('success',success);
  $('notice').hidden=false;
  noticeTimer=setTimeout(() => { $('notice').hidden=true; },success?5000:12000);
}
function connected(ok) {
  $('connection-text').textContent=ok?'Станция онлайн':'Нет связи со Станцией';
  $('connection-dot').classList.toggle('online',ok);
  $('connection-dot').classList.toggle('offline',!ok);
}
async function request(path, body) {
 const controller=new AbortController();const timeout=setTimeout(()=>controller.abort(),48000);
 try {
  const response=await fetch(path,{method:body?'POST':'GET',headers:body?{'Content-Type':'application/json','X-Ytm-Client':'web'}:{},body:body?JSON.stringify(body):undefined,cache:'no-store',signal:controller.signal});
  const data=await response.json();connected(true);
  if(data.state)renderState(data.state);
  if(data.auth){if(state){state.auth=data.auth;state.logged_in=data.auth.configured;}renderAuth(data.auth);}
  if(!response.ok||data.error)throw new Error(data.error||'Не удалось выполнить запрос.');
  return data;
 }finally{clearTimeout(timeout);}
}
async function command(action,options={}) {
  pending++;
  try { return await request('/api/command',{action,...options}); }
  finally { pending--; }
}
async function perform(button, task) {
  if (button) button.disabled=true;
  try { return await task(); }
  catch(error) { notice((error.name==='TimeoutError'||error.name==='AbortError')?'Колонка не ответила вовремя. Повтори запрос.':error.message||'Нет связи с колонкой.'); }
  finally { if(button) button.disabled=false; if(state) renderState(state); }
}
function artworkURL(value) {
  if(!value) return '/placeholder.svg';
  try {
    const url=new URL(value);
    const allowed=['ytimg.com','googleusercontent.com','ggpht.com'];
    if(url.protocol==='https:' && !url.username && !url.password && !url.port && allowed.some(h => url.hostname===h || url.hostname.endsWith('.'+h))) return url.href;
  } catch {}
  return '/placeholder.svg';
}
function setArtwork(image,value) {
  const url=artworkURL(value);
  if(image.getAttribute('src')!==url) image.src=url;
  image.onerror=() => { image.onerror=null; image.src='/placeholder.svg'; };
}
function art(value,alt='Обложка') {
  const image=document.createElement('img');
  image.alt=alt; image.loading='lazy'; image.decoding='async'; image.referrerPolicy='no-referrer';
  setArtwork(image,value);
  return image;
}
function empty(container,message,accountButton=false) {
  container.replaceChildren();
  const p=document.createElement('p'); p.className='empty-state'; p.textContent=message; container.append(p);
  if(accountButton){ const button=document.createElement('button');button.className='secondary-button';button.textContent='Подключить YouTube Music';button.onclick=()=>showView('settings');container.append(button); }
}
function renderTracks(container,tracks,kind) {
  container.replaceChildren();
  if(!tracks.length) { empty(container,'Здесь пока нет треков.'); return; }
  const fragment=document.createDocumentFragment();
  tracks.forEach((track,index) => {
    const row=document.createElement('button'); row.className='track-row';row.type='button';row.dataset.trackId=track.id;row.dataset.index=String(index);
    row.setAttribute('aria-label',`Слушать ${track.title} — ${track.artists||'YouTube Music'}`);
    const number=document.createElement('span');number.className='track-number';number.textContent=String(index+1);
    const text=document.createElement('span');text.className='track-text';
    const title=document.createElement('span');title.className='track-name';title.textContent=track.title;
    const artist=document.createElement('span');artist.className='track-artist';artist.textContent=track.artists||'YouTube Music';text.append(title,artist);
    const duration=document.createElement('span');duration.className='track-duration';duration.textContent=formatTime(track.duration_ms);
    const play=document.createElement('span');play.className='row-play';play.append(icon('play'));play.setAttribute('aria-hidden','true');
    row.append(number,art(track.thumbnail_url),text,duration,play);
    row.onclick=()=>perform(row,async()=>{
      if(kind==='queue') await command('jump',{index:index+1,query:track.id});
      else if(kind==='liked') {
        // Reload likes immediately before selecting: another client may have searched.
        const result=await command('liked');
        const selected=result.tracks.findIndex(t=>t.id===track.id);
        if(selected<0) throw new Error('Список лайков изменился. Обнови его.');
        await command('select',{index:selected+1,query:track.id});
      } else await command('select',{index:index+1,query:track.id});
    });
    fragment.append(row);
  });
  container.append(fragment);
  markCurrent();
}
function markCurrent() {
  const id=state?.queue?.[state.index]?.id;
  document.querySelectorAll('.track-row').forEach(row=>row.classList.toggle('selected',row.dataset.trackId===id));
}
function renderState(next) {
  state=next;
  const track=state.queue?.[state.index];
  $('current-title').textContent=track?.title||'Что включим?';
  $('current-artist').textContent=track?.artists||'Найди трек или попроси Алису';
  $('player-status').textContent=statusText[state.status]||state.status;
  setArtwork($('current-cover'),track?.thumbnail_url);
  const playing=['playing','buffering','searching'].includes(state.status);
  $('toggle').replaceChildren(icon(playing?'pause':'play'));$('toggle').setAttribute('aria-label',playing?'Пауза':'Продолжить');
  $('toggle').disabled=!track;$('stop').disabled=!track;$('previous').disabled=!track||state.index===0;$('next').disabled=!track||state.index>=state.queue.length-1;
  $('progress').max=Math.max(1,track?.duration_ms||1);$('progress').value=state.position_ms||0;
  $('elapsed').textContent=formatTime(state.position_ms);$('duration').textContent=formatTime(track?.duration_ms);
  $('queue-caption').textContent=track?`${state.queue.length} треков · выбран ${state.index+1}`:'Выбери музыку для начала';
  $('announce').checked=state.announce;
  $('sidebar-version').textContent=`ytm-alice ${state.version}`;
  if(state.auth)renderAuth(state.auth);
  $('logout-auth').disabled=!state.logged_in;
  const signature=JSON.stringify(state.queue);
  if(signature!==queueSignature) {queueSignature=signature;renderTracks($('queue-list'),state.queue||[],'queue');}
  markCurrent();
}
function renderAuth(auth) {
 const valid=auth.checked&&auth.valid;
 const identity=auth.account?`${auth.account.auth_user}:${auth.account.page_id||''}`:'';
 const key=`${auth.configured}:${auth.expired||false}:${identity}`;
 if(accountKey&&accountKey!==key){loadedLiked=false;loadedPlaylists=false;}
 accountKey=key;
 const name=auth.account?.name;
 $('auth-state').textContent=!auth.configured?'Подключи YouTube Music для лайков и личных плейлистов.':auth.expired?'Сессия истекла. Загрузи новый cookies.txt — очередь и настройки сохранятся.':valid?`Подключён профиль ${name||'YouTube Music'}${auth.account?.handle?' · '+auth.account.handle:''}.`:'Проверяем вход в YouTube Music…';
 $('auth-badge').textContent=valid?'Подключён':auth.expired?'Обнови вход':auth.configured?'Проверка':'Не подключён';
 $('auth-badge').classList.toggle('good',valid);$('auth-badge').classList.toggle('expired',!!auth.expired);
 $('account-short-label').textContent=valid?(name||'YouTube Music'):auth.expired?'Обновить вход':'YouTube Music';
 $('account-short-label').title=name||'YouTube Music';
 $('logout-auth').disabled=!auth.configured;$('choose-account').disabled=!auth.configured;
 if(valid&&!previousAuth?.valid)$('login-guide').open=false;
 if(auth.expired)$('login-guide').open=true;
 previousAuth=auth;
}
async function loadLiked() {
  if(!state?.logged_in){empty($('liked-list'),'Для личных лайков подключи аккаунт YouTube Music.',true);return;}
  empty($('liked-list'),'Загружаем любимые треки…');
  try {const result=await command('liked');renderTracks($('liked-list'),result.tracks||[],'liked');loadedLiked=true;}
  catch(error){empty($('liked-list'),error.message,true);throw error;}
}
async function loadPlaylists() {
  if(!state?.logged_in){empty($('playlist-list'),'Для личных плейлистов подключи аккаунт YouTube Music.',true);return;}
  empty($('playlist-list'),'Загружаем плейлисты…');
  try {
    const result=await command('playlists');const lists=result.playlists||[];const container=$('playlist-list');container.replaceChildren();
    if(!lists.length) empty(container,'Плейлистов пока нет.');
    lists.forEach(list=>{
      const card=document.createElement('button');card.className='playlist-card';card.setAttribute('aria-label',`Включить плейлист ${list.title}`);
      const title=document.createElement('strong');title.textContent=list.title;
      const caption=document.createElement('small');caption.textContent='Слушать на колонке';caption.append(icon('arrow'));
      card.append(art(list.thumbnail_url,`Обложка плейлиста ${list.title}`),title,caption);
      card.onclick=()=>perform(card,async()=>{await command('playlist',{query:list.id});showView('queue');});container.append(card);
    });loadedPlaylists=true;
  } catch(error){empty($('playlist-list'),error.message,true);throw error;}
}
function showView(view) {
  if(!titles[view]) return;
  activeView=view;
  $('view-title').textContent=titles[view];
  document.querySelectorAll('.view').forEach(section=>section.hidden=section.id!=='view-'+view);
  document.querySelectorAll('nav [data-view]').forEach(button=>{const selected=button.dataset.view===view;button.classList.toggle('active',selected);if(selected)button.setAttribute('aria-current','page');else button.removeAttribute('aria-current');});
  if(view==='search') $('search-input').focus();
  if(view==='liked'&&!loadedLiked) perform(null,loadLiked);
  if(view==='playlists'&&!loadedPlaylists) perform(null,loadPlaylists);
  if(view==='settings'&&state?.logged_in) perform(null,()=>command('auth-check'));
}
async function poll() {
  if(document.hidden || pending) {schedulePoll();return;}
  try {const result=await request('/api/state');if(result.state.error) notice(result.state.error);}
  catch {connected(false);}
  schedulePoll();
}
function schedulePoll(){clearTimeout(refreshTimer);refreshTimer=setTimeout(poll,document.hidden?15000:3000);}

$('search-form').addEventListener('submit',event=>{
  event.preventDefault();const query=$('search-input').value.trim();if(!query)return;
  perform($('search-submit'),async()=>{empty($('search-results'),'Ищем в YouTube Music…');try{const data=await command('search',{query});renderTracks($('search-results'),data.tracks||[],'search');}catch(error){empty($('search-results'),error.message);throw error;}});
});
for(const action of ['previous','next','stop','toggle']) $(action).onclick=()=>perform($(action),()=>command(action));
$('account-shortcut').onclick=()=>showView('settings');
document.querySelectorAll('[data-view]').forEach(button=>button.onclick=()=>showView(button.dataset.view));
$('refresh-liked').onclick=()=>perform($('refresh-liked'),loadLiked);
$('refresh-playlists').onclick=()=>perform($('refresh-playlists'),loadPlaylists);
$('announce').onchange=()=>perform(null,async()=>{try{await command($('announce').checked?'announce-on':'announce-off');}catch(error){$('announce').checked=!!state?.announce;throw error;}});
$('check-auth').onclick=()=>perform($('check-auth'),async()=>{await command('auth-check');notice('Вход в YouTube Music подтверждён.',true);});
$('logout-auth').onclick=()=>perform($('logout-auth'),async()=>{await command('auth-logout');if(importCredentials)importCredentials.cookie='';importCredentials=null;discoveredAccounts=[];$('cookie-file').value='';$('account-choice').hidden=true;loadedLiked=false;loadedPlaylists=false;notice('Аккаунт отключён от колонки.',true);await request('/api/state');});
function showAccounts(accounts) {
 discoveredAccounts=accounts;
 const select=$('account-select');select.replaceChildren();
 for(let i=0;i<accounts.length;i++){
  const account=accounts[i];const option=document.createElement('option');option.value=String(i);
  option.textContent=account.name+(account.handle?' · '+account.handle:'');option.selected=!!account.selected;
  select.append(option);
 }
 $('account-choice').hidden=false;
}
$('cookie-file').onchange=()=>{importCredentials=null;discoveredAccounts=[];$('account-choice').hidden=true;};
$('auth-form').addEventListener('submit',event=>{
 event.preventDefault();
 perform($('import-auth'),async()=>{
  const file=$('cookie-file').files[0];if(!file)throw new Error('Выбери файл cookies.txt.');
  if(file.size>524288)throw new Error('Файл слишком большой. Экспортируй только cookies YouTube.');
  importCredentials=parseCookieFile(await file.text());
  const button=$('import-auth');button.textContent='Ищем аккаунты…';
  try{const result=await command('auth-discover',{auth:importCredentials});showAccounts(result.accounts||[]);}
  catch(error){importCredentials=null;throw error;}
  finally{button.textContent='Показать аккаунты';}
 });
});
$('choose-account').onclick=()=>perform($('choose-account'),async()=>{
 importCredentials=null;const result=await command('auth-accounts');showAccounts(result.accounts||[]);
});
$('connect-account').onclick=()=>perform($('connect-account'),async()=>{
 const selected=discoveredAccounts[Number($('account-select').value)];if(!selected)throw new Error('Сначала выбери профиль YouTube.');
 if(importCredentials){
  const auth={...importCredentials,auth_user:selected.auth_user,page_id:selected.page_id||''};
  try{await command('auth-import',{auth});}finally{auth.cookie='';}
 }else await command('auth-select',{query:selected.auth_user+':'+(selected.page_id||'')});
 if(importCredentials)importCredentials.cookie='';importCredentials=null;discoveredAccounts=[];
 $('cookie-file').value='';$('account-choice').hidden=true;loadedLiked=false;loadedPlaylists=false;
 notice('Подключён профиль '+selected.name+'.',true);await request('/api/state');
});
document.addEventListener('visibilitychange',()=>{clearTimeout(refreshTimer);if(!document.hidden)poll();else schedulePoll();});
async function init(){
 try {
  await request('/api/state');
  if(location.hash==='#settings')showView('settings');
 }catch(error){connected(false);notice(error.message||'Не удалось подключиться к колонке.');schedulePoll();return;}
 if(state?.logged_in)await perform(null,()=>command('auth-check'));else renderAuth({configured:false});
 schedulePoll();
}
init();
