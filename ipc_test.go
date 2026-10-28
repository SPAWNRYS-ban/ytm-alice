package main

import (
	"fmt"
	"net"
	"strings"
	"testing"
)

func aliceStateFrame(state uint64, text, requestID string, final bool) []byte {
	flag := uint64(0)
	if final {
		flag = 1
	}
	asr := cat(pb(1, []byte(text)), pi(2, flag), pi(4, 1))
	stateBody := cat(pi(1, state), pb(2, []byte(text)), pb(7, []byte(requestID)), pb(15, asr))
	return cat(pb(252, stateBody), pi(253, 3))
}

func TestFinalRecognitionIsAvailableBeforeBusy(t *testing.T) {
	const requestID = "12345678-1234-1234-1234-123456789012"
	for _, state := range []uint64{1, 3} {
		event := decodeAlice(aliceStateFrame(state, "Что сейчас играет", requestID, true))
		if !event.HasState || !event.Final || event.State != state || event.RequestID != requestID || event.Text != "Что сейчас играет" {
			t.Fatalf("final recognition lost before BUSY: %+v", event)
		}
	}
	if decodeAlice(aliceStateFrame(1, "Что сейчас", requestID, false)).Final {
		t.Fatal("partial recognition treated as final")
	}
}

func TestOwnedSpeechCancelsDialogAndWaitsBeforeVocalizing(t *testing.T) {
	client, native := net.Pipe()
	defer client.Close()
	result := make(chan error, 1)
	go func() {
		defer native.Close()
		readMethod := func() (map[int]protoField, error) {
			frame, err := frameRead(native)
			if err != nil {
				return nil, err
			}
			return proto(proto(frame)[252].b), nil
		}
		subscribe, err := readMethod()
		if err != nil || len(subscribe[20].b) == 0 {
			result <- fmt.Errorf("missing event subscription: %v", err)
			return
		}
		cancel, err := readMethod()
		if err != nil || proto(cancel[8].b)[1].n != 3 {
			result <- fmt.Errorf("dialog was not cancelled before speech: %v", err)
			return
		}
		// The native dialog remains BUSY briefly; the custom response must wait.
		if err := frameWrite(native, aliceStateFrame(3, "", "", false)); err != nil {
			result <- err
			return
		}
		if err := frameWrite(native, aliceStateFrame(0, "", "", false)); err != nil {
			result <- err
			return
		}
		vocalize, err := readMethod()
		method := proto(vocalize[8].b)
		if err != nil || method[1].n != 18 || string(proto(method[13].b)[1].b) != "Artist. Track" {
			result <- fmt.Errorf("wrong owned vocalization: %v", err)
			return
		}
		for _, event := range []uint64{6, 1} {
			if err := frameWrite(native, cat(pb(252, pb(18, pi(1, event))), pi(253, 210))); err != nil {
				result <- err
				return
			}
		}
		result <- nil
	}()
	if err := speakOnConnection(client, "Artist. Track", true); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestUnconfirmedCancellationDoesNotQueueAnotherAnswer(t *testing.T) {
	client, native := net.Pipe()
	defer client.Close()
	go func() {
		defer native.Close()
		frameRead(native) // subscribe
		frameRead(native) // cancel
		frameWrite(native, aliceStateFrame(3, "", "", false))
	}()
	if err := speakOnConnection(client, "Artist. Track", true); err == nil || !strings.Contains(err.Error(), "cancellation not confirmed") {
		t.Fatalf("speech queued without cancellation confirmation: %v", err)
	}
}
