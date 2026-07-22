package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	meowcaller "github.com/purpshell/meowcaller"
	"github.com/purpshell/meowcaller/diag"
	"github.com/rs/zerolog"
)

const browserVideoFrameDuration = time.Second / 15

type webCallState struct {
	Event       string `json:"event"`
	CallID      string `json:"call_id,omitempty"`
	Peer        string `json:"peer,omitempty"`
	Phase       int    `json:"phase,omitempty"`
	Video       bool   `json:"video,omitempty"`
	VideoState  int    `json:"video_state"`
	Orientation int    `json:"orientation,omitempty"`
	Message     string `json:"message,omitempty"`
	Emoji       string `json:"emoji,omitempty"`
	Sender      string `json:"sender,omitempty"`
	Removed     bool   `json:"removed,omitempty"`
}

type webCallController struct {
	ctx    context.Context
	client *meowcaller.Client
	bridge *videoBridge
	log    zerolog.Logger

	mu      sync.Mutex
	call    *meowcaller.Call
	pending *meowcaller.Call
}

func newWebCallController(ctx context.Context, client *meowcaller.Client, bridge *videoBridge, log zerolog.Logger) *webCallController {
	c := &webCallController{ctx: ctx, client: client, bridge: bridge, log: log}
	bridge.OnControl(c.control)
	bridge.OnFrame(c.sendVideoFrame)
	client.OnIncomingCall(c.onIncomingCall)
	return c
}

func (c *webCallController) publish(state webCallState) {
	c.bridge.PublishState(state)
	c.log.Info().
		Str("event", state.Event).
		Str("call_id", state.CallID).
		Str("peer", state.Peer).
		Bool("video", state.Video).
		Int("video_state", state.VideoState).
		Str("message", state.Message).
		Msg("web call console state")
}

func (c *webCallController) publishReaction(state webCallState) {
	c.bridge.PublishEvent(state)
	c.log.Info().Str("event", state.Event).Str("call_id", state.CallID).
		Str("sender", state.Sender).Str("emoji", state.Emoji).Bool("removed", state.Removed).
		Msg("web call console reaction")
}

func (c *webCallController) onIncomingCall(call *meowcaller.Call) {
	c.mu.Lock()
	if c.call != nil || c.pending != nil {
		c.mu.Unlock()
		_ = call.Reject()
		return
	}
	c.pending = call
	c.mu.Unlock()
	c.publish(webCallState{
		Event: "incoming", CallID: call.ID(), Peer: call.Peer().String(), Video: call.IsVideo(),
	})
}

func (c *webCallController) attach(call *meowcaller.Call) error {
	call.ReceiveVideo(meowcaller.VideoSinkFunc(c.bridge.WriteFrame))
	call.OnVideoKeyframeRequest(c.bridge.RequestKeyframe)
	call.OnPeerAccept(c.bridge.RequestKeyframe)
	call.OnReaction(func(reaction meowcaller.CallReaction) {
		c.publishReaction(webCallState{
			Event: "reaction", CallID: call.ID(), Peer: call.Peer().String(),
			Emoji: reaction.Emoji, Sender: reaction.Sender.String(), Removed: reaction.Removed,
		})
	})
	call.OnVideoState(func(state meowcaller.VideoState) {
		c.bridge.SetOrientation(state.Orientation)
		c.publish(webCallState{
			Event: "video_state", CallID: call.ID(), Peer: call.Peer().String(),
			Video: call.IsVideo(), VideoState: state.Raw, Orientation: state.Orientation,
		})
	})
	call.OnReady(func() {
		c.publish(webCallState{Event: "ready", CallID: call.ID(), Peer: call.Peer().String(), Video: call.IsVideo()})
	})
	call.OnStateChange(func(phase meowcaller.CallPhase) {
		c.publish(webCallState{
			Event: "phase", CallID: call.ID(), Peer: call.Peer().String(), Phase: int(phase), Video: call.IsVideo(),
		})
	})
	call.OnEnd(func(reason string) {
		c.mu.Lock()
		if c.call == call {
			c.call = nil
		}
		if c.pending == call {
			c.pending = nil
		}
		c.mu.Unlock()
		c.publish(webCallState{Event: "ended", CallID: call.ID(), Peer: call.Peer().String(), Message: reason})
	})
	if err := wireMic(call); err != nil {
		return err
	}
	if err := wireSpeaker(call); err != nil {
		return err
	}
	return nil
}

func (c *webCallController) sendVideoFrame(accessUnit []byte) {
	c.mu.Lock()
	call := c.call
	c.mu.Unlock()
	if call == nil || !call.IsVideo() {
		return
	}
	if err := call.SendVideoWithDuration(accessUnit, browserVideoFrameDuration); err != nil {
		c.log.Debug().Err(err).Str("call_id", call.ID()).Msg("browser video frame was not sent")
	}
}

func (c *webCallController) control(command vbControl) error {
	switch command.Action {
	case "dial_audio":
		return c.dial(command.Target, false)
	case "dial_video":
		return c.dial(command.Target, true)
	case "answer":
		return c.answer()
	case "reject":
		return c.reject()
	case "start_video":
		call, err := c.activeCall()
		if err != nil {
			return err
		}
		return call.StartVideo()
	case "accept_video":
		call, err := c.activeCall()
		if err != nil {
			return err
		}
		return call.AcceptVideo()
	case "stop_video":
		call, err := c.activeCall()
		if err != nil {
			return err
		}
		return call.StopVideo()
	case "orientation":
		call, err := c.activeCall()
		if err != nil {
			return err
		}
		return call.SetVideoOrientation(command.Orientation)
	case "reaction":
		call, err := c.activeCall()
		if err != nil {
			return err
		}
		if err = call.SendReaction(command.Emoji); err != nil {
			return err
		}
		c.publishReaction(webCallState{
			Event: "reaction", CallID: call.ID(), Peer: call.Peer().String(),
			Emoji: command.Emoji, Sender: "self", Removed: command.Emoji == "",
		})
		return nil
	case "hangup":
		call, err := c.activeCall()
		if err != nil {
			return err
		}
		return call.Hangup()
	default:
		return fmt.Errorf("unknown action %q", command.Action)
	}
}

func (c *webCallController) activeCall() (*meowcaller.Call, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.call == nil {
		return nil, errors.New("no active call")
	}
	return c.call, nil
}

func (c *webCallController) dial(target string, video bool) error {
	if target == "" {
		return errors.New("target is required")
	}
	c.mu.Lock()
	busy := c.call != nil || c.pending != nil
	c.mu.Unlock()
	if busy {
		return errors.New("another call is already active")
	}
	call, err := c.client.CallWithOptions(c.ctx, target, meowcaller.CallOptions{Video: video})
	if err != nil {
		return err
	}
	if err = c.attach(call); err != nil {
		_ = call.Hangup()
		return err
	}
	c.mu.Lock()
	c.call = call
	c.mu.Unlock()
	c.publish(webCallState{Event: "dialing", CallID: call.ID(), Peer: call.Peer().String(), Video: video})
	return nil
}

func (c *webCallController) answer() error {
	c.mu.Lock()
	call := c.pending
	if call != nil {
		c.pending = nil
		c.call = call
	}
	c.mu.Unlock()
	if call == nil {
		return errors.New("no incoming call")
	}
	if err := c.attach(call); err != nil {
		_ = call.Reject()
		return err
	}
	if err := call.Answer(); err != nil {
		return err
	}
	c.publish(webCallState{Event: "answering", CallID: call.ID(), Peer: call.Peer().String(), Video: call.IsVideo()})
	return nil
}

func (c *webCallController) reject() error {
	c.mu.Lock()
	call := c.pending
	c.pending = nil
	c.mu.Unlock()
	if call == nil {
		return errors.New("no incoming call")
	}
	return call.Reject()
}

func runWebConsole(ctx context.Context, rec *diag.Recorder) error {
	bridge, err := newVideoBridge(*zerolog.Ctx(ctx))
	if err != nil {
		return err
	}
	defer bridge.Close()
	zerolog.Ctx(ctx).Info().Str("url", bridge.URL()).Msg("web call console ready")
	wa, client, err := connectManagedClient(ctx, rec, func(code string, validFor time.Duration) {
		if err := bridge.SetQRCode(code); err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("failed to render pairing QR")
			return
		}
		bridge.PublishState(webCallState{Event: "pairing", Message: validFor.Round(time.Second).String()})
	})
	if err != nil {
		return err
	}
	defer wa.Disconnect()
	newWebCallController(ctx, client, bridge, *zerolog.Ctx(ctx))
	bridge.PublishState(webCallState{Event: "idle", Message: "connected"})
	<-ctx.Done()
	return nil
}
