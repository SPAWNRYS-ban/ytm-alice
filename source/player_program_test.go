package main

import (
	"context"
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
