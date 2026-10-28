package main

import (
	"context"
	"errors"
	"log"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

const mediaName = "org.mpris.MediaPlayer2.ytm_alice"
const mediaPath = dbus.ObjectPath("/org/mpris/MediaPlayer2")
const mediaRootInterface = "org.mpris.MediaPlayer2"
const mediaPlayerInterface = mediaRootInterface + ".Player"

type mediaPlayer struct{ server *Server }

func (m *mediaPlayer) command(action, query string) *dbus.Error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if result := m.server.command(ctx, request{Action: action, Query: query}); result.Error != "" {
		return dbus.MakeFailedError(errors.New(result.Error))
	}
	return nil
}
func (m *mediaPlayer) Play() *dbus.Error                            { return m.command("resume", "") }
func (m *mediaPlayer) Pause() *dbus.Error                           { return m.command("pause", "") }
func (m *mediaPlayer) PlayPause() *dbus.Error                       { return m.command("toggle", "") }
func (m *mediaPlayer) Stop() *dbus.Error                            { return m.command("stop", "") }
func (m *mediaPlayer) Next() *dbus.Error                            { return m.command("next", "") }
func (m *mediaPlayer) Previous() *dbus.Error                        { return m.command("previous", "") }
func (m *mediaPlayer) OpenUri(uri string) *dbus.Error               { return m.command("play", uri) }
func (*mediaPlayer) SeekOffset(int64) *dbus.Error                   { return mediaUnsupported() }
func (*mediaPlayer) SetPosition(dbus.ObjectPath, int64) *dbus.Error { return mediaUnsupported() }

type mediaRoot struct{}

func (mediaRoot) Raise() *dbus.Error { return mediaUnsupported() }
func (mediaRoot) Quit() *dbus.Error  { return mediaUnsupported() }
func mediaUnsupported() *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.NotSupported", []any{"Not supported"})
}

type mediaProperties struct{ server *Server }

func (m *mediaProperties) values(iface string) map[string]dbus.Variant {
	if iface == mediaRootInterface {
		return map[string]dbus.Variant{
			"Identity": dbus.MakeVariant("YouTube Music"), "CanQuit": dbus.MakeVariant(false),
			"CanRaise": dbus.MakeVariant(false), "HasTrackList": dbus.MakeVariant(false),
			"SupportedUriSchemes": dbus.MakeVariant([]string{"http", "https"}),
			"SupportedMimeTypes":  dbus.MakeVariant([]string{"audio/mp4"}),
		}
	}
	if iface != mediaPlayerInterface {
		return nil
	}
	s := m.server.snapshot()
	status := "Stopped"
	if s.Status == "playing" {
		status = "Playing"
	} else if s.Status == "paused" {
		status = "Paused"
	}
	metadata := map[string]dbus.Variant{}
	hasTrack := s.Index >= 0 && s.Index < len(s.Queue)
	if hasTrack {
		t := s.Queue[s.Index]
		path := dbus.ObjectPath(string(mediaPath) + "/track/id_" + strings.ReplaceAll(t.ID, "-", "_"))
		metadata["mpris:trackid"] = dbus.MakeVariant(path)
		metadata["mpris:length"] = dbus.MakeVariant(t.DurationMS * 1000)
		metadata["xesam:title"] = dbus.MakeVariant(t.Title)
		metadata["xesam:artist"] = dbus.MakeVariant([]string{t.Artists})
		metadata["xesam:url"] = dbus.MakeVariant("https://music.youtube.com/watch?v=" + t.ID)
	}
	// Volume is intentionally left to the shared native ALSA Master, avoiding
	// applying both a player gain and the Station's system gain to one command.
	return map[string]dbus.Variant{
		"PlaybackStatus": dbus.MakeVariant(status), "Metadata": dbus.MakeVariant(metadata),
		"Position": dbus.MakeVariant(s.PositionMS * 1000), "Rate": dbus.MakeVariant(float64(1)),
		"MinimumRate": dbus.MakeVariant(float64(1)), "MaximumRate": dbus.MakeVariant(float64(1)),
		"CanControl": dbus.MakeVariant(true), "CanSeek": dbus.MakeVariant(false),
		"CanPlay": dbus.MakeVariant(hasTrack), "CanPause": dbus.MakeVariant(hasTrack),
		"CanGoNext":     dbus.MakeVariant(hasTrack && s.Index+1 < len(s.Queue)),
		"CanGoPrevious": dbus.MakeVariant(hasTrack && s.Index > 0),
	}
}
func (m *mediaProperties) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	values := m.values(iface)
	if values == nil {
		return nil, dbus.NewError("org.freedesktop.DBus.Error.UnknownInterface", nil)
	}
	return values, nil
}
func (m *mediaProperties) Get(iface, property string) (dbus.Variant, *dbus.Error) {
	if value, ok := m.values(iface)[property]; ok {
		return value, nil
	}
	return dbus.Variant{}, dbus.NewError("org.freedesktop.DBus.Error.UnknownProperty", nil)
}
func (*mediaProperties) Set(string, string, dbus.Variant) *dbus.Error {
	return dbus.NewError("org.freedesktop.DBus.Error.PropertyReadOnly", nil)
}

func (s *Server) mediaSession(ctx context.Context, address string) error {
	conn, err := dbus.Connect(address, dbus.WithContext(ctx))
	if err != nil {
		return err
	}
	defer conn.Close()
	player := &mediaPlayer{server: s}
	properties := &mediaProperties{server: s}
	root := mediaRoot{}
	for iface, object := range map[string]any{
		mediaRootInterface: root, mediaPlayerInterface: player,
		"org.freedesktop.DBus.Properties": properties,
	} {
		names := map[string]string{}
		if iface == mediaPlayerInterface {
			names["SeekOffset"] = "Seek"
		}
		if err := conn.ExportWithMap(object, names, mediaPath, iface); err != nil {
			return err
		}
	}
	node := &introspect.Node{Name: string(mediaPath)}
	for iface, methods := range map[string]any{mediaRootInterface: root, mediaPlayerInterface: player} {
		entry := introspect.Interface{Name: iface, Methods: introspect.Methods(methods)}
		for i := range entry.Methods {
			if entry.Methods[i].Name == "SeekOffset" {
				entry.Methods[i].Name = "Seek"
			}
		}
		for name, value := range properties.values(iface) {
			entry.Properties = append(entry.Properties, introspect.Property{Name: name, Type: value.Signature().String(), Access: "read"})
		}
		node.Interfaces = append(node.Interfaces, entry)
	}
	if err := conn.Export(introspect.NewIntrospectable(node), mediaPath, "org.freedesktop.DBus.Introspectable"); err != nil {
		return err
	}
	owned, err := conn.RequestName(mediaName, dbus.NameFlagDoNotQueue)
	if err != nil || owned != dbus.RequestNameReplyPrimaryOwner {
		return errors.New("MPRIS name unavailable")
	}
	s.mediaConnected.Store(true)
	defer s.mediaConnected.Store(false)
	log.Print("MPRIS media interface ready")
	previous := properties.values(mediaPlayerInterface)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-conn.Context().Done():
			return errors.New("MPRIS bus disconnected")
		case <-ticker.C:
			current := properties.values(mediaPlayerInterface)
			changed := map[string]dbus.Variant{}
			for key, value := range current {
				if key != "Position" && !reflect.DeepEqual(previous[key], value) {
					changed[key] = value
				}
			}
			previous = current
			if len(changed) > 0 {
				if err := conn.Emit(mediaPath, "org.freedesktop.DBus.Properties.PropertiesChanged", mediaPlayerInterface, changed, []string{}); err != nil {
					return err
				}
			}
		}
	}
}

func (s *Server) mediaLoop(ctx context.Context) {
	for ctx.Err() == nil {
		address := os.Getenv("YTM_ALICE_MPRIS_BUS")
		if address == "" {
			if _, err := os.Stat("/tmp/btbus/socket"); err == nil {
				address = "unix:path=/tmp/btbus/socket"
			}
		}
		if address != "" {
			if err := s.mediaSession(ctx, address); err != nil && ctx.Err() == nil {
				log.Print("MPRIS bridge reconnecting")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}
