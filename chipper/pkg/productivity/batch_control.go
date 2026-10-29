package productivity

import (
	"context"
	"strings"

	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
)

// Keep the robot under SDK control while a batch is being read. Releasing it
// between notifications lets normal behaviors change the head's viewing angle.
type reminderBatchControl struct {
	robot      *vector.Vector
	robotESN   string
	generation uint64
	ctx        context.Context
	cancel     context.CancelFunc
	stream     behaviorControlStream
	held       bool
}

func (control *reminderBatchControl) matches(task Task) bool {
	return control.robot != nil && control.ctx != nil && control.ctx.Err() == nil &&
		strings.EqualFold(control.robotESN, task.RobotESN) && control.generation == task.configurationGeneration
}

// The stream lives for the batch, while each task still has its own deadline.
// Join the cancellation watcher before canceling a completed task's context so
// its cleanup cannot cancel the stream being reused by the next notification.
func (control *reminderBatchControl) acquire(taskCtx context.Context, robot *vector.Vector, task Task) (func(), error) {
	if !control.matches(task) {
		control.close()
		control.robot, control.robotESN, control.generation = robot, task.RobotESN, task.configurationGeneration
		control.ctx, control.cancel = context.WithCancel(context.Background())
	}
	stopWatching := watchReminderControlTask(taskCtx, control.cancel)
	var err error
	if control.stream == nil {
		control.stream, err = robot.Conn.BehaviorControl(control.ctx)
	}
	if err == nil && !control.held {
		err = acquireBehaviorControl(control.stream)
		control.held = err == nil
	}
	if err != nil {
		stopWatching()
		control.close()
		return nil, err
	}
	return stopWatching, nil
}

func watchReminderControlTask(taskCtx context.Context, cancel context.CancelFunc) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-taskCtx.Done():
			cancel()
		case <-stop:
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

func (control *reminderBatchControl) release() error {
	if !control.held {
		return nil
	}
	control.held = false
	return releaseBehaviorControl(control.stream)
}

func (control *reminderBatchControl) close() {
	if control.ctx != nil && control.ctx.Err() == nil {
		if err := control.release(); err != nil {
			logger.Println("Productivity: Failed to release notification batch control: " + err.Error())
		}
	}
	if control.cancel != nil {
		control.cancel()
	}
	*control = reminderBatchControl{}
}
