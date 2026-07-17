package meowcaller

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/purpshell/meowcaller/rtp"
	"google.golang.org/protobuf/encoding/protowire"
)

const (
	appDataRetransmitCount = 10
	appDataTimestampStep   = 50
	appDataRetransmitDelay = 50 * time.Millisecond
)

var errAppDataUnavailable = errors.New("meowcaller: call app-data stream is unavailable")

type appDataReaction struct {
	transactionID uint64
	emoji         string
}

func encodeAppDataReaction(transactionID uint64, emoji string) []byte {
	reaction := protowire.AppendTag(nil, 1, protowire.VarintType)
	reaction = protowire.AppendVarint(reaction, transactionID)
	reaction = protowire.AppendTag(reaction, 2, protowire.BytesType)
	reaction = protowire.AppendString(reaction, emoji)

	message := protowire.AppendTag(nil, 1, protowire.BytesType)
	message = protowire.AppendBytes(message, reaction)

	payloads := protowire.AppendTag(nil, 1, protowire.BytesType)
	return protowire.AppendBytes(payloads, message)
}

func decodeAppDataReactions(payload []byte) ([]appDataReaction, error) {
	var reactions []appDataReaction
	for len(payload) > 0 {
		num, typ, n := protowire.ConsumeTag(payload)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		payload = payload[n:]
		if num == 1 && typ == protowire.BytesType {
			message, n := protowire.ConsumeBytes(payload)
			if n < 0 {
				return nil, protowire.ParseError(n)
			}
			payload = payload[n:]
			reaction, ok, err := decodeAppDataMessage(message)
			if err != nil {
				return nil, err
			}
			if ok {
				reactions = append(reactions, reaction)
			}
			continue
		}
		n = protowire.ConsumeFieldValue(num, typ, payload)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		payload = payload[n:]
	}
	return reactions, nil
}

func decodeAppDataMessage(message []byte) (appDataReaction, bool, error) {
	for len(message) > 0 {
		num, typ, n := protowire.ConsumeTag(message)
		if n < 0 {
			return appDataReaction{}, false, protowire.ParseError(n)
		}
		message = message[n:]
		if num == 1 && typ == protowire.BytesType {
			reaction, n := protowire.ConsumeBytes(message)
			if n < 0 {
				return appDataReaction{}, false, protowire.ParseError(n)
			}
			decoded, err := decodeReactionInfo(reaction)
			return decoded, err == nil, err
		}
		n = protowire.ConsumeFieldValue(num, typ, message)
		if n < 0 {
			return appDataReaction{}, false, protowire.ParseError(n)
		}
		message = message[n:]
	}
	return appDataReaction{}, false, nil
}

func decodeReactionInfo(message []byte) (appDataReaction, error) {
	var reaction appDataReaction
	for len(message) > 0 {
		num, typ, n := protowire.ConsumeTag(message)
		if n < 0 {
			return appDataReaction{}, protowire.ParseError(n)
		}
		message = message[n:]
		switch {
		case num == 1 && typ == protowire.VarintType:
			reaction.transactionID, n = protowire.ConsumeVarint(message)
		case num == 2 && typ == protowire.BytesType:
			var raw []byte
			raw, n = protowire.ConsumeBytes(message)
			if n >= 0 {
				if !utf8.Valid(raw) {
					return appDataReaction{}, errors.New("meowcaller: reaction is not valid UTF-8")
				}
				reaction.emoji = string(raw)
			}
		default:
			n = protowire.ConsumeFieldValue(num, typ, message)
		}
		if n < 0 {
			return appDataReaction{}, protowire.ParseError(n)
		}
		message = message[n:]
	}
	if reaction.transactionID == 0 {
		return appDataReaction{}, errors.New("meowcaller: reaction has no transaction ID")
	}
	return reaction, nil
}

type appDataReceiver struct {
	mu                sync.Mutex
	lastTransactionID uint64
}

func (r *appDataReceiver) receive(payload []byte) (appDataReaction, bool, error) {
	reactions, err := decodeAppDataReactions(payload)
	if err != nil {
		return appDataReaction{}, false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, reaction := range reactions {
		if reaction.transactionID <= r.lastTransactionID {
			continue
		}
		r.lastTransactionID = reaction.transactionID
		return reaction, true, nil
	}
	return appDataReaction{}, false, nil
}

type appDataSender struct {
	mu                 sync.Mutex
	pipe               *MediaPipeline
	ssrc               uint32
	sendPacket         func([]byte) (int, error)
	sequenceNumber     uint16
	timestamp          uint32
	transactionID      uint64
	retransmitInterval time.Duration
}

func newAppDataSender(pipe *MediaPipeline, ssrc uint32, sendPacket func([]byte) (int, error)) *appDataSender {
	return &appDataSender{
		pipe:               pipe,
		ssrc:               ssrc,
		sendPacket:         sendPacket,
		retransmitInterval: appDataRetransmitDelay,
	}
}

func (s *appDataSender) sendReaction(emoji string) error {
	if !utf8.ValidString(emoji) {
		return errors.New("meowcaller: reaction is not valid UTF-8")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pipe == nil || s.sendPacket == nil {
		return errAppDataUnavailable
	}
	s.transactionID++
	payload := encodeAppDataReaction(s.transactionID, emoji)
	for i := 0; i < appDataRetransmitCount; i++ {
		s.sequenceNumber++
		s.timestamp += appDataTimestampStep
		header := &rtp.RtpHeader{
			PayloadType:    rtp.RtpPayloadTypeAppData,
			SequenceNumber: s.sequenceNumber,
			Timestamp:      s.timestamp,
			Ssrc:           s.ssrc,
		}
		packet, err := s.pipe.ProtectRTP(header, payload)
		if err != nil {
			return fmt.Errorf("protect call reaction: %w", err)
		}
		if _, err = s.sendPacket(packet); err != nil {
			return fmt.Errorf("send call reaction: %w", err)
		}
		if i+1 < appDataRetransmitCount && s.retransmitInterval > 0 {
			time.Sleep(s.retransmitInterval)
		}
	}
	return nil
}

func (s *appDataSender) close() {
	s.mu.Lock()
	s.sendPacket = nil
	s.mu.Unlock()
}

type mediaPayloadKind uint8

const (
	mediaPayloadUnknown mediaPayloadKind = iota
	mediaPayloadAudio
	mediaPayloadVideo
	mediaPayloadAppData
)

func classifyMediaPayload(header rtp.RtpHeader) mediaPayloadKind {
	switch {
	case header.PayloadType == rtp.RtpPayloadTypeAppData:
		return mediaPayloadAppData
	case header.PayloadType == rtp.RtpPayloadTypeH264:
		return mediaPayloadVideo
	case rtp.IsWhatsappOpusRtpPayload(header.PayloadType):
		return mediaPayloadAudio
	default:
		return mediaPayloadUnknown
	}
}
