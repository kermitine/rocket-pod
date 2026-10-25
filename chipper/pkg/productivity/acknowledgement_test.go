package productivity

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"google.golang.org/grpc"
)

type fakeAcknowledgementStream struct {
	grpc.ClientStream
	ctx      context.Context
	sent     []*vectorpb.ExternalAudioStreamRequest
	response *vectorpb.ExternalAudioStreamResponse
	failSend int
	sendErr  error
	recvErr  error
	closed   bool
}

func (stream *fakeAcknowledgementStream) Send(request *vectorpb.ExternalAudioStreamRequest) error {
	stream.sent = append(stream.sent, request)
	if len(stream.sent) == stream.failSend {
		return stream.sendErr
	}
	return nil
}

func (stream *fakeAcknowledgementStream) Recv() (*vectorpb.ExternalAudioStreamResponse, error) {
	if stream.response != nil || stream.recvErr != nil {
		return stream.response, stream.recvErr
	}
	<-stream.ctx.Done()
	return nil, stream.ctx.Err()
}

func (stream *fakeAcknowledgementStream) CloseSend() error {
	stream.closed = true
	return nil
}

type acknowledgementRobotClient struct {
	vectorpb.ExternalInterfaceClient
	stream *fakeAcknowledgementStream
	err    error
}

func (client *acknowledgementRobotClient) ExternalAudioStreamPlayback(ctx context.Context, _ ...grpc.CallOption) (vectorpb.ExternalInterface_ExternalAudioStreamPlaybackClient, error) {
	client.stream.ctx = ctx
	return client.stream, client.err
}

func successfulAcknowledgementStream() *fakeAcknowledgementStream {
	return &fakeAcknowledgementStream{response: &vectorpb.ExternalAudioStreamResponse{
		AudioResponseType: &vectorpb.ExternalAudioStreamResponse_AudioStreamPlaybackComplete{
			AudioStreamPlaybackComplete: &vectorpb.ExternalAudioStreamPlaybackComplete{},
		},
	}}
}

func TestCommandAcknowledgementStreamsChimeWithoutBehaviorControl(t *testing.T) {
	stream := successfulAcknowledgementStream()
	// Other SDK methods are absent: a receipt must not ask for battery
	// readings, behavior control, animations, or movement.
	if err := playVoiceCommandAcknowledgement(context.Background(), &acknowledgementRobotClient{stream: stream}); err != nil {
		t.Fatal(err)
	}
	prepare := stream.sent[0].GetAudioStreamPrepare()
	if prepare.GetAudioFrameRate() != 16000 || prepare.GetAudioVolume() == 0 || prepare.GetAudioVolume() > 100 {
		t.Fatalf("invalid audio format or volume: %v", prepare)
	}
	var pcm []byte
	for _, request := range stream.sent[1 : len(stream.sent)-1] {
		chunk := request.GetAudioStreamChunk()
		if chunk == nil || len(chunk.AudioChunkSamples) == 0 || len(chunk.AudioChunkSamples) > 1024 || int(chunk.AudioChunkSizeBytes) != len(chunk.AudioChunkSamples) {
			t.Fatalf("invalid audio chunk: %v", chunk)
		}
		pcm = append(pcm, chunk.AudioChunkSamples...)
	}
	if len(pcm) != 8000 || !bytes.Equal(pcm, commandAcknowledgementPCM()) {
		t.Fatalf("quarter-second chime was truncated or corrupted: %d bytes", len(pcm))
	}
	if stream.sent[len(stream.sent)-1].GetAudioStreamComplete() == nil || !stream.closed {
		t.Fatal("audio stream was not completed and closed")
	}
	var firstNote, secondNote bool
	for offset := 0; offset < len(pcm); offset += 2 {
		sample := int16(binary.LittleEndian.Uint16(pcm[offset:]))
		if sample < -9000 || sample > 9000 {
			t.Fatalf("sound clips at byte %d", offset)
		}
		switch {
		case offset < 3520:
			firstNote = firstNote || sample != 0
		case offset < 4480:
			if sample != 0 {
				t.Fatal("notes have no silent gap")
			}
		default:
			secondNote = secondNote || sample != 0
		}
	}
	if !firstNote || !secondNote {
		t.Fatal("one acknowledgement note is silent")
	}
}

func TestCommandAcknowledgementHandlesPlaybackFailures(t *testing.T) {
	wantErr := errors.New("speaker unavailable")
	for _, send := range []int{1, 2, 9, 10} {
		stream := successfulAcknowledgementStream()
		stream.failSend, stream.sendErr = send, wantErr
		if err := playVoiceCommandAcknowledgement(context.Background(), &acknowledgementRobotClient{stream: stream}); !errors.Is(err, wantErr) {
			t.Fatalf("send %d error = %v", send, err)
		}
		if !stream.closed || len(stream.sent) != send {
			t.Fatal("failed stream was left open or continued sending")
		}
	}
	for _, response := range []*vectorpb.ExternalAudioStreamResponse{
		{AudioResponseType: &vectorpb.ExternalAudioStreamResponse_AudioStreamPlaybackFailyer{AudioStreamPlaybackFailyer: &vectorpb.ExternalAudioStreamPlaybackFailure{}}},
		{AudioResponseType: &vectorpb.ExternalAudioStreamResponse_AudioStreamBufferOverrun{AudioStreamBufferOverrun: &vectorpb.ExternalAudioStreamBufferOverrun{}}},
	} {
		stream := &fakeAcknowledgementStream{response: response}
		if err := playVoiceCommandAcknowledgement(context.Background(), &acknowledgementRobotClient{stream: stream}); err == nil || !stream.closed {
			t.Fatal("robot playback failure was ignored or left the stream open")
		}
	}
	stream := &fakeAcknowledgementStream{recvErr: io.EOF}
	if err := playVoiceCommandAcknowledgement(context.Background(), &acknowledgementRobotClient{stream: stream}); !errors.Is(err, io.EOF) {
		t.Fatalf("incomplete playback error = %v", err)
	}
	if err := playVoiceCommandAcknowledgement(context.Background(), &acknowledgementRobotClient{stream: stream, err: wantErr}); !errors.Is(err, wantErr) {
		t.Fatalf("stream creation error = %v", err)
	}
}

func TestCommandAcknowledgementStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	stream := &fakeAcknowledgementStream{}
	if err := playVoiceCommandAcknowledgement(ctx, &acknowledgementRobotClient{stream: stream}); !errors.Is(err, context.DeadlineExceeded) || !stream.closed {
		t.Fatalf("unresponsive playback did not stop: %v", err)
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	select {
	case <-startVoiceCommandAcknowledgement(canceled, "canceled-command"):
	case <-time.After(time.Second):
		t.Fatal("canceled acknowledgement blocked its response")
	}
}
