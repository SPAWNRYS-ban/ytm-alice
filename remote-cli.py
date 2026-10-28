#!/usr/bin/env python3
"""Terminal player connected directly to a Station; Python standard library only."""
import argparse
import atexit
import curses
import configparser
import http.cookiejar
from http.cookies import SimpleCookie, CookieError
import json
import locale
import os
import queue
import select
import shlex
import shutil
import sqlite3
import subprocess
import sys
import threading
import tempfile
import time
import uuid
import urllib.request
import urllib.error
import urllib.parse
from pathlib import Path

VERSION = '0.7.0'
REMOTE = '/data/yam/bin/ytm-alice'
STATES = {'playing': 'Играет', 'paused': 'Пауза', 'stopped': 'Остановлено',
          'searching': 'Поиск', 'buffering': 'Подготовка', 'error': 'Ошибка'}
ALIASES = {'p': 'play', 'n': 'next', 'b': 'previous', 'prev': 'previous',
           's': 'status', 'q': 'queue', 'find': 'search', 'ui': 'tui'}
COMMANDS = {'connect', 'play', 'pause', 'resume', 'toggle', 'next', 'previous', 'stop',
            'status', 'queue', 'search', 'select', 'jump', 'tui',
            'announce-on', 'announce-off', 'daemon', 'say', 'probe', 'web',
            'login', 'logout', 'auth', 'liked', 'playlists', 'playlist'}


class ClientError(Exception):
    def __init__(self, message, response=None):
        super().__init__(message)
        self.response = response


def config_path():
    return Path(os.environ.get('YTM_ALICE_CONFIG', Path.home()/'.config/ytm-alice/config.json'))


def normalize_url(value):
    if not isinstance(value, str):
        raise ClientError('Адрес Станции должен быть строкой, например http://alice.lan.')
    try:
        parsed = urllib.parse.urlsplit(value)
        valid = (parsed.scheme == 'http' and parsed.hostname and not parsed.username
                 and not parsed.password and parsed.path in ('', '/')
                 and not parsed.query and not parsed.fragment and parsed.port != 0)
    except ValueError:
        valid = False
    if not valid or any(ord(ch) < 33 for ch in value):
        raise ClientError('Укажи HTTP-адрес Станции, например http://alice.lan или http://IP.')
    return value.rstrip('/')


def connection_url(explicit=None):
    if explicit or os.environ.get('YTM_ALICE_URL'):
        return normalize_url(explicit or os.environ['YTM_ALICE_URL'])
    path = config_path()
    try:
        saved = json.loads(path.read_text()) if path.is_file() else {}
        return normalize_url(saved.get('url', 'http://alice.lan'))
    except (OSError, ValueError, AttributeError) as error:
        raise ClientError('Не удалось прочитать настройки подключения: '+str(path)) from error


def save_connection(value):
    value = normalize_url(value)
    path = config_path()
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode='w', dir=path.parent, delete=False) as output:
            temporary = Path(output.name)
            json.dump({'url': value}, output)
            output.write('\n')
        temporary.replace(path)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)
    return value


def ssh_command():
    config = Path(os.environ.get('ALICE_CLI_SSH_CONFIG',
                                 Path.home()/'.config/alice-cli/ssh.json'))
    try:
        command = json.loads(config.read_text()) if config.is_file() else [
            'ssh', '-o', 'BatchMode=yes', os.environ.get('ALICE_SSH', 'alice')]
    except (OSError, ValueError) as error:
        raise ClientError('Не удалось прочитать настройки SSH: '+str(error)) from error
    if not isinstance(command, list) or not command or not all(isinstance(x, str) for x in command):
        raise ClientError('Настройки SSH должны содержать массив аргументов.')
    # Options precede the destination and any existing options in the saved argv.
    return command[:1]+['-o', 'BatchMode=yes', '-o', 'ConnectTimeout=5',
                       '-o', 'ServerAliveInterval=10', '-o', 'ServerAliveCountMax=3']+command[1:]


class Client:
    def __init__(self, transport=None, base_url=None, ssh=False):
        self.transport = transport
        self.base_url = None if transport is not None or ssh else connection_url(base_url)
        self.process = None
        self.closed = False
        self.lock = threading.Lock()
        self.buffer = bytearray()
        atexit.register(self.close)

    def close(self):
        self.closed = True
        self.disconnect()
        atexit.unregister(self.close)

    def disconnect(self):
        process, self.process = self.process, None
        self.buffer.clear()
        if process is not None:
            if process.poll() is None:
                process.terminate()
                try:process.wait(timeout=1)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
            process.stdin.close()
            process.stdout.close()

    def request(self, action='status', query_text='', index=None, auth=None):
        with self.lock:
            if self.closed:
                raise ClientError('Клиент Станции закрыт.')
            if self.base_url is not None:
                return self.http_request(action, query_text, index, auth)
            if self.process is None:
                command = self.transport
                if command is None:
                    ssh = ssh_command()
                    command = ssh[:1]+['-T']+ssh[1:]+['exec '+shlex.join([REMOTE, '--rpc'])]
                self.process = subprocess.Popen(command, stdin=subprocess.PIPE,
                                                stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
            process = self.process
            if self.closed or process.poll() is not None:
                self.disconnect()
                raise ClientError('Не удалось подключиться к колонке. Проверьте SSH-доступ.')
            request = {'action': action, 'query': query_text}
            if index is not None:request['index'] = index
            if auth is not None:request['auth'] = auth
            try:
                process.stdin.write((json.dumps(request, ensure_ascii=False)+'\n').encode())
                process.stdin.flush()
                deadline = time.monotonic()+55
                while b'\n' not in self.buffer:
                    remaining = deadline-time.monotonic()
                    if remaining <= 0 or not select.select([process.stdout], [], [], max(0, remaining))[0]:
                        raise ClientError('Колонка не ответила за 55 секунд.')
                    block = os.read(process.stdout.fileno(), 65536)
                    if not block:
                        raise ClientError('Нет ответа от колонки. Проверьте SSH-доступ и версию ytm-alice.')
                    self.buffer.extend(block)
                    if len(self.buffer) > 1 << 20:
                        raise ClientError('Слишком большой ответ от колонки.')
                line, _, rest = self.buffer.partition(b'\n')
                self.buffer = bytearray(rest)
                result = json.loads(line)
                if not isinstance(result, dict):raise ValueError('expected an object')
            except (OSError, ValueError, ClientError) as error:
                self.disconnect()
                if isinstance(error, ClientError):raise
                raise ClientError('Ошибка обмена с колонкой: '+str(error)) from error
            if result.get('error'):
                raise ClientError(result['error'], result)
            return result

    def http_request(self, action, query_text, index, auth):
        payload = {'action': action, 'query': query_text}
        if index is not None:payload['index'] = index
        if auth is not None:payload['auth'] = auth
        request = urllib.request.Request(self.base_url+'/api/command',
            data=json.dumps(payload, ensure_ascii=False).encode(),
            headers={'Content-Type':'application/json', 'Origin':self.base_url, 'X-Ytm-Client':'web'})
        try:
            try:
                response = urllib.request.build_opener(NoRedirect()).open(request, timeout=55)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                raw = response.read((1 << 20)+1)
                if len(raw) > 1 << 20:raise ClientError('Слишком большой ответ от Станции.')
                result = json.loads(raw)
                if not isinstance(result, dict):raise ValueError('expected an object')
                if result.get('error'):raise ClientError(result['error'], result)
                if response.status != 200:raise ClientError('Станция вернула HTTP '+str(response.status))
                return result
        except ClientError:
            raise
        except (OSError, ValueError) as error:
            raise ClientError('Нет ответа от '+self.base_url+'. Проверь адрес: ytm-alice connect http://IP_СТАНЦИИ') from error


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, new_url):
        # Never forward session cookies or playback commands to another address.
        return None


def panel_state(result):
    """Adapt native state for the original terminal view; JSON stays unchanged."""
    state = dict(result.get('state') or {})
    tracks = state.get('queue') or []
    index = state.get('index', -1)
    state.update(state=state.get('status', 'stopped'), queue=tracks, position=index,
                 current=tracks[index] if 0 <= index < len(tracks) else None)
    return state


YOUTUBE_HOSTS = {'.youtube.com', 'youtube.com', '.music.youtube.com',
                 'music.youtube.com', 'www.youtube.com'}


def has_session(cookie):
    try:
        parsed = SimpleCookie()
        parsed.load(cookie)
        return any(name in parsed and parsed[name].value for name in ('__Secure-3PAPISID', 'SAPISID'))
    except CookieError:
        return False


def firefox_cookie(profile):
    """Read only the default container's YouTube cookies, never Google domains."""
    database = Path(profile)/'cookies.sqlite'
    if not database.is_file():return None
    sql = """SELECT name,value,host FROM moz_cookies
             WHERE host IN ('.youtube.com','youtube.com','.music.youtube.com','music.youtube.com','www.youtube.com')
             AND originAttributes='' AND (expiry=0 OR expiry>?) ORDER BY length(host),lastAccessed"""

    def read(path):
        connection = sqlite3.connect(path.as_uri()+'?mode=ro', uri=True, timeout=0.2)
        try:
            connection.execute('PRAGMA query_only=ON')
            return connection.execute(sql, (int(time.time()),)).fetchall()
        finally:connection.close()

    try:rows = read(database)
    except sqlite3.Error:
        # Firefox may hold an exclusive lock. The private snapshot is removed
        # immediately; only YouTube rows leave this function.
        try:
            with tempfile.TemporaryDirectory(prefix='ytm-alice-firefox-') as folder:
                target = Path(folder)/'cookies.sqlite'
                shutil.copyfile(database, target)
                target.chmod(0o600)
                wal = Path(str(database)+'-wal')
                if wal.is_file():
                    copy_wal = Path(str(target)+'-wal')
                    shutil.copyfile(wal, copy_wal)
                    copy_wal.chmod(0o600)
                rows = read(target)
        except (OSError, sqlite3.Error) as error:
            raise ClientError('Не удалось прочитать cookies Firefox; закрой Firefox и повтори login.') from error
    values = {name: value for name, value, host in rows}
    cookie = '; '.join(name+'='+value for name, value in values.items())
    return cookie if has_session(cookie) else None


def firefox_session(profile=None):
    if profile and Path(profile).expanduser().is_dir():
        cookie = firefox_cookie(Path(profile).expanduser().resolve())
        if not cookie:raise ClientError('В выбранном профиле нет входа в YouTube Music.')
        return cookie
    candidates = []
    for root in (Path.home()/'.mozilla/firefox', Path.home()/'.config/mozilla/firefox'):
        ini = root/'profiles.ini'
        if not ini.is_file():continue
        config = configparser.ConfigParser()
        config.read(ini)
        defaults = {config[s].get('Default') for s in config.sections() if s.startswith('Install')}
        for section in config.sections():
            if not section.startswith('Profile'):continue
            item = config[section]
            label = item.get('Name', section)
            if profile and profile not in (label, section, item.get('Path')):continue
            location = Path(item['Path'])
            if item.get('IsRelative', '1') == '1':location = root/location
            cookie = firefox_cookie(location.resolve())
            if cookie:
                preferred = item.get('Default') == '1' or item['Path'] in defaults
                candidates.append((label, cookie, preferred))
    if not candidates:
        raise ClientError('В Firefox не найден вход YouTube. Открой music.youtube.com, войди и повтори login.')
    preferred = [c for c in candidates if c[2]]
    if len(preferred) == 1:return preferred[0][1]
    if len(candidates) == 1:return candidates[0][1]
    names = ', '.join(c[0] for c in candidates)
    raise ClientError('Несколько профилей со входом: '+names+'. Выбери login --profile ИМЯ.')


def login_credentials(args):
    account = '0'
    if args.cookies:
        jar = http.cookiejar.MozillaCookieJar(str(Path(args.cookies).expanduser()))
        try:jar.load(ignore_discard=True)
        except (OSError, http.cookiejar.LoadError) as error:
            raise ClientError('Не удалось прочитать Netscape cookies-файл.') from error
        cookie = '; '.join(c.name+'='+c.value for c in jar if c.domain in YOUTUBE_HOSTS)
    elif args.headers:
        try:
            raw = Path(args.headers).expanduser().read_text()
            if raw.lstrip().startswith('{'):
                headers = {k.lower(): v for k, v in json.loads(raw).items()}
            else:
                headers = {key.lower(): value.strip() for line in raw.splitlines()
                           if ':' in line for key, value in [line.split(':', 1)]}
            cookie = headers.get('cookie', '')
            account = str(headers.get('x-goog-authuser', headers.get('auth_user', '0')))
        except (OSError, ValueError, AttributeError) as error:
            raise ClientError('Не удалось прочитать файл заголовков YouTube Music.') from error
    else:cookie = firefox_session(args.profile)
    if args.account_index is not None:account = str(args.account_index)
    if not isinstance(cookie, str) or not has_session(cookie):
        raise ClientError('В cookies нет активной сессии YouTube Music.')
    return {'cookie': cookie, 'auth_user': account}


def stamp(ms):
    seconds=max(0,int(ms or 0)//1000)
    return f'{seconds//60}:{seconds%60:02d}'


def duration_text(ms):
    return stamp(ms) if ms else '—:—'


def label(track):
    if not track:return 'Нет выбранного трека'
    return (track.get('artists','')+' — ' if track.get('artists') else '')+track.get('title','Без названия')


def rows(tracks, current=-1):
    return [f'{"▶" if i == current else " "} {i+1:>3}. {label(t)}  [{duration_text(t.get("duration_ms"))}]'
            for i,t in enumerate(tracks)]


def human(action, result):
    if 'auth' in result:
        auth = result['auth']
        if not auth['configured']:print('YouTube Music: вход не выполнен. ytm-alice login')
        elif auth['checked'] and auth['valid']:print('YouTube Music: вход подтверждён, аккаунт подключён.')
        else:print('YouTube Music: сессия сохранена. Проверка: ytm-alice auth check')
        return
    if action=='playlists':
        playlists = result.get('playlists', [])
        for i, playlist in enumerate(playlists, 1):
            print(f'{i:>3}. {playlist["title"]}\n     ytm-alice playlist {playlist["id"]}')
        if not playlists:print('В библиотеке нет плейлистов.')
        return
    result = panel_state(result) if action not in ('search','liked') else result
    if action in ('search','liked'):
        tracks=result.get('tracks',[])
        print('\n'.join(rows(tracks[:15])) if tracks else 'Ничего не найдено.')
        print('Для запуска: ytm-alice select N')
        return
    if action=='queue':
        print('Очередь:')
        print('\n'.join(rows(result.get('queue',[]),result.get('position',-1))) or 'Пусто')
        return
    track=result.get('current')
    print(f'{STATES.get(result.get("state"),result.get("state",""))} · {label(track)}')
    if track:
        print(f'{stamp(result.get("position_ms"))} / {duration_text(track.get("duration_ms"))}  ·  '
              f'трек {result.get("position",0)+1} из {len(result.get("queue",[]))}')
    if result.get('error'):print('Ошибка: '+result['error'])


def panel(screen, client):
    locale.setlocale(locale.LC_ALL,'')
    try:curses.curs_set(0)
    except curses.error:pass
    try:curses.use_default_colors()
    except curses.error:pass
    curses.set_escdelay(30)
    if curses.has_colors():
        curses.init_pair(1,curses.COLOR_CYAN,-1);curses.init_pair(2,curses.COLOR_GREEN,-1)
        curses.init_pair(3,curses.COLOR_YELLOW,-1);curses.init_pair(4,curses.COLOR_RED,-1)
    screen.timeout(100)
    state={'state':'stopped','queue':[],'position':-1}
    results=[]; mode='queue'; selected=0; top=0
    prompt=None; text=''; message='Подключение…'; error=False
    foreground=False; polling=False; next_poll=0; snapshot=time.monotonic()
    inbox=queue.Queue(); started=0

    def submit(kind, action='status', query_text='', index=None):
        def worker():
            try:inbox.put((kind,action,client.request(action,query_text,index),None))
            except Exception as e:inbox.put((kind,action,None,str(e)))
        threading.Thread(target=worker,daemon=True).start()

    def command(action,query_text='',index=None):
        nonlocal foreground,message,error,started
        if foreground:return
        foreground=True;started=time.monotonic();error=False
        message='Поиск…' if action=='search' else 'Выполняю…'
        submit('command',action,query_text,index)

    def put(y,x,value,attr=0):
        height,width=screen.getmaxyx()
        if 0<=y<height and 0<=x<width-1:
            try:screen.addnstr(y,x,value,max(0,width-x-1),attr)
            except curses.error:pass

    while True:
        now=time.monotonic()
        while not inbox.empty():
            kind,action,result,err=inbox.get()
            if kind=='poll':polling=False
            else:foreground=False
            if err:
                message=err;error=True
            elif action in ('search','liked','playlists'):
                results=result.get('playlists' if action=='playlists' else 'tracks',[])
                mode='playlists' if action=='playlists' else 'search';selected=0;top=0
                message=f'Найдено: {len(results)}. Enter — включить, Esc — очередь.';error=False
            else:
                old_id=(state.get('current') or {}).get('id')
                state=panel_state(result);snapshot=now
                if kind=='command' or message=='Подключение…' or error:message='Готово';error=False
                if state.get('error'):message=state['error'];error=True
                if mode=='queue' and old_id!=(state.get('current') or {}).get('id'):
                    selected=max(0,state.get('position',0))
            next_poll=now+(3 if err else 1)
        if not foreground and not polling and now>=next_poll:
            polling=True;submit('poll')
        height,width=screen.getmaxyx();screen.erase()
        cyan=curses.color_pair(1) if curses.has_colors() else curses.A_BOLD
        green=curses.color_pair(2) if curses.has_colors() else 0
        yellow=curses.color_pair(3) if curses.has_colors() else 0
        red=curses.color_pair(4) if curses.has_colors() else 0
        put(0,2,'ytm-alice  ·  YouTube Music'+('  ·  сессия аккаунта' if state.get('logged_in') else '  ·  без входа'),cyan|curses.A_BOLD)
        if height<14 or width<45:
            put(2,1,'Увеличьте терминал. q — выход.')
        else:
            track=state.get('current'); status=state.get('state','stopped')
            put(2,2,label(track),curses.A_BOLD)
            put(3,2,STATES.get(status,status),green if status=='playing' else yellow)
            pos=state.get('position_ms',0)
            if status=='playing':pos+=int((now-snapshot)*1000)
            duration=(track or {}).get('duration_ms',0)
            if duration:pos=min(pos,duration)
            bar=max(8,min(width-24,60));fill=int(bar*min(1,pos/duration)) if duration else 0
            put(4,2,'['+'━'*fill+'─'*(bar-fill)+'] '+stamp(pos)+' / '+duration_text(duration))
            title={'search':'Результаты поиска','playlists':'Мои плейлисты'}.get(mode,'Очередь')
            items=results if mode!='queue' else state.get('queue',[])
            put(6,2,f'{title} · {len(items)}',cyan)
            count=max(1,height-12);selected=max(0,min(selected,max(0,len(items)-1)))
            if selected<top:top=selected
            if selected>=top+count:top=selected-count+1
            for line,index in enumerate(range(top,min(len(items),top+count))):
                current=mode=='queue' and index==state.get('position',-1)
                prefix='▶' if current else ' '
                value=f'{prefix} {index+1:>3}  {label(items[index])}  {duration_text(items[index].get("duration_ms"))}'
                put(7+line,2,value,curses.A_REVERSE if index==selected else 0)
            notice=message
            if foreground:notice=f'{"|/-\\"[int(now*5)%4]} {message}  {now-started:.0f} с'
            put(height-4,2,notice,red if error else yellow)
            put(height-3,2,'↑↓ выбор  Enter играть  Space пауза  n/b треки  s стоп',cyan)
            put(height-2,2,'/ поиск  p запрос  l лайки  L плейлисты  Esc очередь  q выход',cyan)
            if prompt:
                put(height-4,2,' '*(width-3))
                put(height-4,2,('Поиск: ' if prompt=='search' else 'Включить: ')+text+'▏',curses.A_BOLD)
        screen.refresh()
        try:key=screen.get_wch()
        except curses.error:continue
        if prompt:
            if key=='\x1b':prompt=None;text=''
            elif key in ('\n','\r',curses.KEY_ENTER):
                if text.strip():command(prompt,text.strip())
                prompt=None;text=''
            elif key in ('\b','\x7f',curses.KEY_BACKSPACE):text=text[:-1]
            elif isinstance(key,str) and key.isprintable() and len(text)<500:text+=key
            continue
        if key in ('q','Q','\x03'):return
        if key=='\x1b':mode='queue';selected=max(0,state.get('position',0));top=0
        elif key in (curses.KEY_DOWN,'j'):selected+=1
        elif key in (curses.KEY_UP,'k'):selected-=1
        elif key==curses.KEY_NPAGE:selected+=max(1,height-12)
        elif key==curses.KEY_PPAGE:selected-=max(1,height-12)
        elif key==curses.KEY_HOME:selected=0
        elif key==curses.KEY_END:selected=9999
        elif key in ('\n','\r',curses.KEY_ENTER) and not foreground:
            if mode=='playlists' and results:
                command('playlist',results[selected]['id']);mode='queue'
            elif mode=='search' and results:
                command('select',index=selected+1)
                mode='queue'
            elif state.get('queue'):command('jump',index=selected+1)
        elif key==' ' and not foreground:command('toggle')
        elif key=='n':command('next')
        elif key=='b':command('previous')
        elif key=='s':command('stop')
        elif key=='/':prompt='search';text=''
        elif key=='p':prompt='play';text=''
        elif key=='l':command('liked')
        elif key=='L':command('playlists')
        elif key=='r':next_poll=0



def run_native(arguments, remote=REMOTE, tty=False):
    ssh = ssh_command()
    command = ssh[:1]+(['-t'] if tty else ['-T'])+ssh[1:]
    return subprocess.run(command+['exec '+shlex.join([remote]+arguments)]).returncode


def yam_main(arguments):
    """Retain the existing YAM v2 launcher, including local package uploads."""
    arguments = list(arguments) or ['--help']
    temporary = None
    ssh = ssh_command()
    try:
        if 'install' in arguments:
            for i in range(arguments.index('install')+1, len(arguments)):
                local = Path(arguments[i])
                if not arguments[i].startswith('-') and local.is_file():
                    temporary = '/tmp/yam-cli-'+uuid.uuid4().hex+'.yampkg'
                    with local.open('rb') as source:
                        subprocess.run(ssh+['umask 077; cat > '+shlex.quote(temporary)],
                                       stdin=source, check=True)
                    arguments[i] = temporary
                    break
        return run_native(arguments, '/usr/bin/yam', sys.stdin.isatty())
    finally:
        if temporary:
            subprocess.run(ssh+['rm -f '+shlex.quote(temporary)], stdout=subprocess.DEVNULL)


def main():
    if Path(sys.argv[0]).name == 'yam':return yam_main(sys.argv[1:])
    parser = argparse.ArgumentParser(
        description='YouTube Music на колонке: команды или интерактивная панель.',
        epilog='Без команды открывается панель. Space — пауза, n/b — треки, / — поиск, q — выход.')
    parser.add_argument('--json', action='store_true', help='JSON для скриптов')
    parser.add_argument('--version', action='version', version='ytm-alice '+VERSION)
    connection = parser.add_mutually_exclusive_group()
    connection.add_argument('--url', help='HTTP-адрес Станции для этой команды')
    connection.add_argument('--ssh', action='store_true', help='Управление через существующий SSH alias alice')
    method = parser.add_mutually_exclusive_group()
    method.add_argument('--browser', choices=['firefox'], help='login: браузер (по умолчанию Firefox)')
    method.add_argument('--cookies', help='login: Netscape cookies-файл')
    method.add_argument('--headers', help='login: browser.json или файл заголовков YouTube Music')
    parser.add_argument('--profile', help='login: имя или путь профиля Firefox')
    parser.add_argument('--account-index', type=int, choices=range(100), metavar='N',
                        help='login: индекс Google-аккаунта, по умолчанию 0')
    parser.add_argument('action', nargs='?', help='connect URL, play, search, select N, jump N, pause, resume, '
                        'toggle, next, previous, stop, status, queue, ui, login, logout, auth status/check, '
                        'liked, playlists, playlist ID, announce-on/off, daemon, web status')
    parser.add_argument('query', nargs='*', help='запрос, ссылка или номер трека, начиная с 1')
    args = parser.parse_args()
    interactive = sys.stdin.isatty() and sys.stdout.isatty()
    action = ALIASES.get(args.action, args.action) or ('tui' if interactive and not args.json else 'status')
    if action not in COMMANDS:parser.error('неизвестная команда: '+action)
    if action != 'login' and any((args.browser, args.cookies, args.headers, args.profile, args.account_index is not None)):
        parser.error('параметры входа доступны только с login')
    text = ' '.join(args.query).strip()
    if action == 'login' and text:parser.error('профиль задаётся через login --profile ИМЯ')
    if args.profile and (args.cookies or args.headers):parser.error('--profile используется только с Firefox')
    if action in ('play', 'search', 'say', 'probe') and not text:
        parser.error('укажите название или ссылку')
    if action == 'daemon' and (len(args.query) != 1 or args.query[0] not in ('start', 'stop', 'restart', 'install', 'uninstall')):
        parser.error('daemon start|stop|restart|install|uninstall')
    if action == 'web' and args.query != ['status']:parser.error('web status')
    if action == 'auth':
        if args.query not in (['status'], ['check']):parser.error('auth status|check')
        action = 'auth-'+args.query[0]
    elif action == 'logout':action = 'auth-logout'
    if action == 'playlist' and not text:parser.error('укажите ID плейлиста из команды playlists')
    index = None
    if action in ('select', 'jump'):
        try:index = int(text)
        except ValueError:parser.error('нужен номер трека, начиная с 1')
        if index < 1:parser.error('нужен номер трека, начиная с 1')
    if action == 'connect':
        try:
            if not text and interactive:text = input('Адрес Станции [http://alice.lan]: ').strip() or 'http://alice.lan'
            if not text:parser.error('укажите адрес: connect http://alice.lan')
            address = save_connection(text)
            print(json.dumps({'url':address}) if args.json else 'Станция: '+address)
            return 0
        except (ClientError, OSError) as error:
            print('ytm-alice: '+str(error), file=sys.stderr)
            return 1
    client = None
    try:
        if action in ('daemon', 'say', 'probe', 'web'):
            return run_native((['--json'] if args.json else [])+[action]+args.query)
        client = Client(base_url=args.url, ssh=args.ssh)
        if action == 'tui':
            if not interactive:raise ClientError('Для панели нужен интерактивный терминал.')
            if args.json:raise ClientError('Панель не поддерживает --json; используйте --json status.')
            locale.setlocale(locale.LC_ALL, '')
            curses.wrapper(panel, client)
            return 0
        if action == 'login':
            credentials = login_credentials(args)
            result = client.request('auth-import', auth=credentials)
        else:result = client.request(action, text, index)
        if args.json:print(json.dumps(result, ensure_ascii=False, indent=2))
        else:human(action, result)
        return 0
    except KeyboardInterrupt:return 130
    except (ClientError, OSError, curses.error) as error:
        if args.json:
            print(json.dumps(getattr(error, 'response', None) or {'error': str(error)}, ensure_ascii=False))
        else:print('ytm-alice: '+str(error), file=sys.stderr)
        return 1
    finally:
        if client is not None:client.close()


if __name__ == '__main__':
    raise SystemExit(main())
