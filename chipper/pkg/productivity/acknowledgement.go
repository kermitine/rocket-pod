package productivity

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

const (
	commandAcknowledgementTimeout    = 3 * time.Second
	commandAcknowledgementSettle     = 150 * time.Millisecond
	commandAcknowledgementSampleRate = 16000
)

// A receipt must not wait for the reminder queue or battery/movement checks.
// Its response waits on the returned channel while the lookup runs in parallel.
func startVoiceCommandAcknowledgement(parent context.Context, robotESN string) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(parent, commandAcknowledgementTimeout)
		defer cancel()
		// Let the voice intent finish before starting speaker playback.
		timer := time.NewTimer(commandAcknowledgementSettle)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if err := playRobotCommandAcknowledgement(ctx, robotESN); err != nil {
			logger.Println("Productivity: Command acknowledgement sound failed: " + err.Error())
		}
	}()
	return done
}

func playRobotCommandAcknowledgement(ctx context.Context, robotESN string) error {
	for _, robot := range vars.BotInfo.Robots {
		if robot.Esn != robotESN {
			continue
		}
		// Match the self-signed SDK connection used by bedtime/readiness
		// monitoring, and close this connection after the short playback.
		conn, err := grpc.DialContext(ctx, net.JoinHostPort(robot.IPAddress, "443"),
			grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})))
		if err != nil {
			return err
		}
		defer conn.Close()
		authCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+robot.GUID)
		return playVoiceCommandAcknowledgement(authCtx, vectorpb.NewExternalInterfaceClient(conn))
	}
	return fmt.Errorf("robot not in botsdkinfo")
}

func playVoiceCommandAcknowledgement(ctx context.Context, client vectorpb.ExternalInterfaceClient) error {
	stream, err := client.ExternalAudioStreamPlayback(ctx)
	if err != nil {
		return err
	}
	defer stream.CloseSend()
	if err := stream.Send(&vectorpb.ExternalAudioStreamRequest{
		AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamPrepare{
			AudioStreamPrepare: &vectorpb.ExternalAudioStreamPrepare{
				AudioFrameRate: commandAcknowledgementSampleRate,
				AudioVolume:    85,
			},
		},
	}); err != nil {
		return err
	}
	pcm := commandAcknowledgementPCM()
	// The short chime fits in the playback buffer. Respect the
	// robot's 1024-byte message limit, including the last partial chunk.
	for offset := 0; offset < len(pcm); offset += 1024 {
		end := offset + 1024
		if end > len(pcm) {
			end = len(pcm)
		}
		chunk := pcm[offset:end]
		if err := stream.Send(&vectorpb.ExternalAudioStreamRequest{
			AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamChunk{
				AudioStreamChunk: &vectorpb.ExternalAudioStreamChunk{
					AudioChunkSizeBytes: uint32(len(chunk)),
					AudioChunkSamples:   chunk,
				},
			},
		}); err != nil {
			return err
		}
	}
	if err := stream.Send(&vectorpb.ExternalAudioStreamRequest{
		AudioRequestType: &vectorpb.ExternalAudioStreamRequest_AudioStreamComplete{
			AudioStreamComplete: &vectorpb.ExternalAudioStreamComplete{},
		},
	}); err != nil {
		return err
	}
	for {
		response, err := stream.Recv()
		if err != nil {
			return err
		}
		if response.GetAudioStreamPlaybackComplete() != nil {
			return nil
		}
		if response.GetAudioStreamPlaybackFailyer() != nil || response.GetAudioStreamBufferOverrun() != nil {
			return fmt.Errorf("robot could not play acknowledgement sound")
		}
	}
}

// Generate two rising notes locally, with soft envelopes to avoid clicks.
func commandAcknowledgementPCM() []byte {
	const noteSamples = commandAcknowledgementSampleRate * 300 / 1000
	const gapSamples = commandAcknowledgementSampleRate * 80 / 1000
	pcm := make([]byte, (2*noteSamples+gapSamples)*2)
	for note, frequency := range []float64{880, 1174.66} {
		for sample := 0; sample < noteSamples; sample++ {
			envelope := math.Sin(math.Pi * float64(sample) / float64(noteSamples-1))
			wave := math.Sin(2 * math.Pi * frequency * float64(sample) / commandAcknowledgementSampleRate)
			value := int16(12000 * envelope * envelope * wave)
			offset := (note*(noteSamples+gapSamples) + sample) * 2
			binary.LittleEndian.PutUint16(pcm[offset:], uint16(value))
		}
	}
	return pcm
}
