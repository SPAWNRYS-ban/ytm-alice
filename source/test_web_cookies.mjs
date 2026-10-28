import {test} from 'node:test';
import assert from 'node:assert/strict';
import {parseCookieFile} from './web/cookies.mjs';
const now=1700000000;
const row=(host,name,value,expiry='2000000000')=>`${host}\tTRUE\t/\tTRUE\t${expiry}\t${name}\t${value}`;
test('imports HttpOnly and YouTube only, ignores expired and unrelated cookies',()=>{
 const auth=parseCookieFile(['# Netscape HTTP Cookie File','#HttpOnly_'+row('.youtube.com','SAPISID','youtube-session'),row('.google.com','SID','never-forward'),row('.youtube.com.evil.invalid','SID','never-forward'),row('.youtube.com','OLD','expired','1'),row('.music.youtube.com','PREF','music-preference')].join('\n'),'1',now);
 assert.deepEqual(auth,{cookie:'SAPISID=youtube-session; PREF=music-preference',auth_user:'1'});
});
test('Music-specific cookies win; session cookies with expiry zero work',()=>{
 const auth=parseCookieFile([row('.music.youtube.com','SAPISID','specific','0'),row('.youtube.com','SAPISID','domain')].join('\n'),'0',now);
 assert.equal(auth.cookie,'SAPISID=specific');
});
test('rejects wrong format, logged-out, malformed account and header injection',()=>{
 for(const source of ['{"cookies":[]}',row('.youtube.com','PREF','anonymous'),row('.youtube.com','SAPISID','x; injected=y')])assert.throws(()=>parseCookieFile(source,'0',now));
 for(const account of ['-1','100','1\nX: injected','1.5'])assert.throws(()=>parseCookieFile(row('.youtube.com','SAPISID','sid'),account,now));
});
test('bounds original files and normalized cookies',()=>{
 assert.throws(()=>parseCookieFile('x'.repeat(524289)));
 assert.throws(()=>parseCookieFile(row('.youtube.com','SAPISID','x'.repeat(48001)),'0',now));
});
