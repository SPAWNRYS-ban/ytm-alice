const youtubeHosts = new Set(['.youtube.com','youtube.com','.music.youtube.com','music.youtube.com','www.youtube.com']);
const token = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/;

export function parseCookieFile(source, account = '0', now = Date.now()/1000) {
  if (typeof source !== 'string' || source.length > 524288) throw new Error('Файл слишком большой: максимум 512 КБ. Экспортируй только cookies YouTube.');
  const accountText = String(account).trim();
  if (!/^(0|[1-9]\d?)$/.test(accountText)) throw new Error('Номер аккаунта должен быть от 0 до 99.');
  const rows = [];
  let malformed = false;
  for (let line of source.replace(/^\uFEFF/, '').split(/\r?\n/)) {
    if (line.startsWith('#HttpOnly_')) line = line.slice(10);
    if (!line || line.startsWith('#')) continue;
    const fields = line.split('\t');
    if (fields.length !== 7) { malformed = true; continue; }
    const [domain, , path, , expiry, name, value] = fields;
    if (!youtubeHosts.has(domain.toLowerCase())) continue;
    if (!/^\d+$/.test(expiry) || (Number(expiry) !== 0 && Number(expiry) <= now)) continue;
    if (!token.test(name) || /[\x00-\x20\x7f;]/.test(value) || !value) continue;
    if (!(path === '/' || path === '')) continue;
    rows.push({domain,name,value});
  }
  if (!rows.length) throw new Error(malformed ? 'Нужен Netscape cookies.txt: выбери этот формат в расширении.' : 'В файле нет действующих cookies YouTube. Войди в YouTube Music и повтори экспорт.');
  // Domain cookies first, Music-specific cookies last, as in the desktop importer.
  rows.sort((a,b) => a.domain.length-b.domain.length);
  const values = new Map();
  for (const row of rows) values.set(row.name, row.value);
  if (!values.has('SAPISID') && !values.has('__Secure-3PAPISID')) throw new Error('Не найдена сессия YouTube. Войди в аккаунт и экспортируй cookies заново.');
  const cookie = [...values].map(([name,value]) => `${name}=${value}`).join('; ');
  if (cookie.length > 48000) throw new Error('Сессия слишком большая. Экспортируй только cookies текущего сайта YouTube.');
  return {cookie,auth_user:accountText};
}
