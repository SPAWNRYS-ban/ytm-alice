package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

// A tiny URL object with the same transform entry point as the YouTube player.
const fixtureURLPlayer = `(function(){
var values;
function URLObject(){this.values={};}
URLObject.prototype.set=function(k,v){this.values[k]=v;};
URLObject.prototype.get=function(k){return this.values[k];};
URLObject.prototype.transform=function(){if(this.values.s!==undefined)this.values.s=this.values.s.split('').reverse().join('');if(this.values.n!==undefined)this.values.n=this.values.n+'-decoded';};
function decipher(base,key,s){var u=new URLObject();u.set('alr','yes');u.set(key,s);return u;}
throwIfNativeStartupRuns();
}).call(this);`

func fixturePlayerVariants() []string {
	return []string{
		fixtureURLPlayer,
		strings.Replace(fixtureURLPlayer, "var values;", "var values=0;values+=1;if(values!==1){throw new Error('compound assignment lost');}", 1),
		strings.Replace(fixtureURLPlayer, "function decipher(base,key,s)", "var decipher=function(base,key,s)", 1),
		strings.Replace(fixtureURLPlayer, "function decipher(base,key,s)", "decipher=function(base,key,s)", 1),
		"var ignored={};" + strings.Replace(fixtureURLPlayer, "}).call(this);", "})();", 1),
		strings.Replace(fixtureURLPlayer, "throwIfNativeStartupRuns();", "if(false){missing();}else if(false){missing();}(throwIfNativeStartupRuns());", 1),
	}
}

func TestPlayerPreprocessingKeepsTransformsWithoutNativeStartup(t *testing.T) {
	for _, player := range fixturePlayerVariants() {
		code, err := preprocessPlayer(player)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(code, "throwIfNativeStartupRuns") || !strings.Contains(code, "_result.sig") || !strings.Contains(code, "_result.n") {
			t.Fatal("startup or missing transforms")
		}
	}
}

func TestPlayerPreprocessingRejectsUnknownStructure(t *testing.T) {
	for _, player := range []string{"", "invalid syntax{", "(function(){var x=1;}).call(this);", "callNativeStartup();"} {
		if _, err := preprocessPlayer(player); err == nil {
			t.Fatal("accepted unknown player")
		}
	}
}

func TestNativePreprocessedSignatureAndNChallenges(t *testing.T) {
	if os.Getenv("YTM_ALICE_RESOLVER") == "" {
		t.Skip("native resolver not available")
	}
	for _, player := range fixturePlayerVariants() {
		code, err := preprocessPlayer(player)
		if err != nil {
			t.Fatal(err)
		}
		result, err := runDecipher(context.Background(), map[string]any{"type": "preprocessed", "preprocessed_player": code, "requests": []any{
			map[string]any{"type": "sig", "challenges": []string{"abcdef"}},
			map[string]any{"type": "n", "challenges": []string{"12345"}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if str(get(result, "responses", 0, "data", "abcdef")) != "fedcba" || str(get(result, "responses", 1, "data", "12345")) != "12345-decoded" {
			t.Fatal("incorrect challenge transformation")
		}
	}
}

func TestNativePublicStreamUsesModernNTransform(t *testing.T) {
	if os.Getenv("YTM_ALICE_RESOLVER") == "" {
		t.Skip("native resolver not available")
	}
	c := newCatalog()
	player := map[string]any{"playabilityStatus": map[string]any{"status": "OK"}, "streamingData": map[string]any{"adaptiveFormats": []any{map[string]any{"itag": 140, "url": "https://test.googlevideo.com/audio?n=12345", "mimeType": "audio/mp4", "audioChannels": 2, "contentLength": "1024"}}}}
	raw, _ := json.Marshal(player)
	c.http = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "music.youtube.com" {
			t.Fatal("public metadata unexpectedly required a session")
		}
		var content string
		switch {
		case strings.HasPrefix(r.URL.Path, "/embed/"):
			content = `<script src="/s/player/test/player_ias.vflset/en_US/base.js"></script>`
		case strings.HasPrefix(r.URL.Path, "/s/player/"):
			content = fixtureURLPlayer
		case r.URL.Path == "/watch":
			content = "var ytInitialPlayerResponse=" + string(raw) + ";"
		default:
			content = string(raw)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(content))}, nil
	})}
	stream, err := c.resolve(context.Background(), "abcdefghijk")
	if err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(stream.url)
	if err != nil || link.Query().Get("n") != "12345-decoded" {
		t.Fatal("public n challenge bypassed current decoder")
	}
}
