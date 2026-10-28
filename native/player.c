#include <gst/gst.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/select.h>

#define PLUGINS(X) X(coreelements) X(playback) X(typefindfunctions) X(audioconvert) X(audioresample) X(alsa_nocapturelog) X(volume) X(audioparsers) X(isomp4) X(libav) X(soup) X(gio) X(audiofx)
#define DECLARE_PLUGIN(name) GST_PLUGIN_STATIC_DECLARE(name);
PLUGINS(DECLARE_PLUGIN)

static int running = 1, result = 0;
static GstElement *play;
static gboolean on_message(GstBus *bus, GstMessage *msg, gpointer unused) {
    (void)bus; (void)unused;
    switch (GST_MESSAGE_TYPE(msg)) {
    case GST_MESSAGE_EOS:
        puts("{\"event\":\"eos\"}"); running = 0; break;
    case GST_MESSAGE_ERROR:
        { GError *error = NULL; gst_message_parse_error(msg, &error, NULL);
          if (error) {fprintf(stderr,"playback: %.300s\n",error->message);g_error_free(error);} }
        puts("{\"event\":\"error\"}"); result = 5; running = 0; break;
    case GST_MESSAGE_STATE_CHANGED:
        if (GST_MESSAGE_SRC(msg) == GST_OBJECT(play)) {
            GstState old, now, pending;
            gst_message_parse_state_changed(msg, &old, &now, &pending);
            if (now == GST_STATE_PLAYING) puts("{\"event\":\"playing\"}");
            if (now == GST_STATE_PAUSED) puts("{\"event\":\"paused\"}");
        } break;
    default: break;
    }
    return TRUE;
}
/* Use the Station's private GStreamer/GLib and native ALSA/dmix. */
int main(int argc, char **argv) {
    if (argc != 2 || strncmp(argv[1], "http://127.0.0.1:", 17)) return 2;
    if (!gst_init_check(NULL, NULL, NULL)) {fputs("gst init failed\n",stderr);return 3;}
    #define REGISTER_PLUGIN(name) GST_PLUGIN_STATIC_REGISTER(name);
    PLUGINS(REGISTER_PLUGIN)
    char description[512];
    snprintf(description, sizeof(description), "playbin uri=\"%s\" audio-sink=\"alsasink device=default\" video-sink=fakesink", argv[1]);
    GError *error = NULL;
    play = gst_parse_launch(description, &error);
    if (!play || error) { fprintf(stderr,"pipeline: %s\n",error ? error->message : "not created"); return 3; }
    GstBus *bus = gst_pipeline_get_bus(GST_PIPELINE(play));
    if (!bus) {fputs("no gst bus\n",stderr);return 3;}
    gst_bus_add_watch(bus, on_message, NULL);
    setvbuf(stdout, NULL, _IOLBF, 0);
    setvbuf(stdin, NULL, _IONBF, 0);
    if (gst_element_set_state(play, GST_STATE_PLAYING) == GST_STATE_CHANGE_FAILURE) {fputs("gst set playing failed\n",stderr);return 4;}
    char line[128];
    gint64 last = -1;
    while (running) {
        fd_set input; FD_ZERO(&input); FD_SET(STDIN_FILENO, &input);
        struct timeval timeout = {0, 100000};
        if (select(STDIN_FILENO+1, &input, NULL, NULL, &timeout) > 0) {
            if (!fgets(line, sizeof(line), stdin)) break;
            if (!strncmp(line, "pause", 5)) gst_element_set_state(play, GST_STATE_PAUSED);
            else if (!strncmp(line, "resume", 6)) gst_element_set_state(play, GST_STATE_PLAYING);
            else if (!strncmp(line, "stop", 4)) break;
            else if (!strncmp(line, "seek ", 5)) {
                gint64 ms = g_ascii_strtoll(line+5, NULL, 10);
                if (ms >= 0 && ms < 7200000) {
                    gboolean ok = gst_element_seek_simple(play, GST_FORMAT_TIME,
                        GST_SEEK_FLAG_FLUSH | GST_SEEK_FLAG_ACCURATE, ms * GST_MSECOND);
                    printf("{\"event\":\"seek\",\"ok\":%s,\"position_ms\":%lld}\n",ok ? "true" : "false",(long long)ms);
                }
            } else if (!strncmp(line, "volume ", 7)) {
                double volume = g_ascii_strtod(line+7, NULL);
                if (volume >= 0 && volume <= 1) g_object_set(play, "volume", volume, NULL);
            }
        }
        while (g_main_context_iteration(NULL, FALSE)) {}
        gint64 pos;
        if (gst_element_query_position(play, GST_FORMAT_TIME, &pos) && pos/GST_SECOND != last) {
            last = pos/GST_SECOND;
            printf("{\"event\":\"position\",\"position_ms\":%lld}\n", (long long)(pos/GST_MSECOND));
        }
    }
    gst_element_set_state(play, GST_STATE_NULL);
    gst_object_unref(bus); gst_object_unref(play);
    return result;
}
