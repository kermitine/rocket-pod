package productivity

import (
	"strings"
	"sync"
)

type sportsNotificationKey struct {
	source, eventID, robotESN string
	generation                uint64
}

type pendingSportsNotification struct {
	key            sportsNotificationKey
	superseded     chan struct{}
	retryScheduled bool // Protected by taskQueueMu.
}

var (
	taskQueueMu                sync.Mutex
	pendingSportsNotifications = make(map[sportsNotificationKey]*pendingSportsNotification)
)

// Serialize producers so replacing a sports update can make room in a full
// queue without dropping unrelated work or racing another producer's send.
func enqueueTask(task Task) bool {
	taskQueueMu.Lock()
	defer taskQueueMu.Unlock()
	if taskIsSuperseded(task) {
		return false
	}
	select {
	case taskQueue <- task:
		return true
	default:
		return false
	}
}

// All automatic notifications for a game/session share a key. A live update
// replaces its pregame reminder, and the final result replaces pending live
// updates and notable moments. Separate robots and sessions keep their own work.
func enqueueSportsNotification(task Task, eventID string) bool {
	if eventID == "" {
		return enqueueTask(task)
	}
	taskQueueMu.Lock()
	if !taskIsCurrent(task) {
		taskQueueMu.Unlock()
		return false
	}
	key := sportsNotificationKey{task.Source, eventID, strings.ToLower(task.RobotESN), task.configurationGeneration}
	previous := pendingSportsNotifications[key]
	pending := make([]Task, 0, cap(taskQueue))
drain:
	for {
		select {
		case queued := <-taskQueue:
			if (previous == nil || queued.sportsNotification != previous) && !taskIsSuperseded(queued) {
				pending = append(pending, queued)
			}
		default:
			break drain
		}
	}

	queueFull := len(pending) == cap(taskQueue)
	accepted := !queueFull || previous != nil
	if accepted {
		if previous != nil {
			close(previous.superseded)
		}
		task.sportsNotification = &pendingSportsNotification{key: key, superseded: make(chan struct{})}
		pendingSportsNotifications[key] = task.sportsNotification
		if !queueFull {
			pending = append(pending, task)
		}
	}
	for _, queued := range pending {
		taskQueue <- queued
	}
	taskQueueMu.Unlock()
	if accepted && queueFull {
		// The previous update is waiting outside the channel (for readiness or
		// delivery). Replace that pending retry even when unrelated work fills
		// the channel, then wait for space without reviving the older update.
		requeueTaskAfter(task, 0)
	}
	return accepted
}

func taskSupersededChannel(task Task) <-chan struct{} {
	if task.sportsNotification == nil {
		return nil
	}
	return task.sportsNotification.superseded
}

func taskIsSuperseded(task Task) bool {
	select {
	case <-taskSupersededChannel(task):
		return true
	default:
		return false
	}
}

func beginSportsTaskAttempt(task Task) {
	if task.sportsNotification != nil {
		taskQueueMu.Lock()
		task.sportsNotification.retryScheduled = false
		taskQueueMu.Unlock()
	}
}

func markSportsTaskDeferred(task Task) {
	if task.sportsNotification != nil {
		taskQueueMu.Lock()
		task.sportsNotification.retryScheduled = true
		taskQueueMu.Unlock()
	}
}

// Keep deferred tasks registered so a newer update can cancel their retry.
// Release completed or abandoned tasks so the registry does not grow per game.
func finishSportsTaskAttempt(task Task) {
	if notification := task.sportsNotification; notification != nil {
		taskQueueMu.Lock()
		defer taskQueueMu.Unlock()
		if !notification.retryScheduled && pendingSportsNotifications[notification.key] == notification {
			delete(pendingSportsNotifications, notification.key)
		}
	}
}

func forgetSportsTask(task Task) {
	if notification := task.sportsNotification; notification != nil {
		taskQueueMu.Lock()
		defer taskQueueMu.Unlock()
		if pendingSportsNotifications[notification.key] == notification {
			delete(pendingSportsNotifications, notification.key)
		}
	}
}
