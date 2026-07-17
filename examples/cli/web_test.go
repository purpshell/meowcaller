package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWebCallStatePreservesDisabledVideoState(t *testing.T) {
	data, err := json.Marshal(webCallState{Event: "video_state", VideoState: 0})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"video_state":0`) {
		t.Fatalf("disabled video state was omitted: %s", data)
	}
}

func TestVideoBridgePageHidesPeerFramesWhileRemoteVideoIsOff(t *testing.T) {
	for _, behavior := range []string{
		"setRemoteVideoActive(false)",
		"setRemoteVideoActive(true)",
		"if(remoteVideoActive)",
	} {
		if !strings.Contains(videoBridgePage, behavior) {
			t.Errorf("page does not contain %q", behavior)
		}
	}
}
