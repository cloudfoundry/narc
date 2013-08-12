package narc

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudfoundry/go_cfmessagebus"
	"github.com/nu7hatch/gouuid"
	"github.com/vito/gordon"
	"log"
	"os/exec"
)

type AgentConfig struct {
	WardenSocketPath string
}

type Agent struct {
	ID       *uuid.UUID
	Registry *Registry
	Config   AgentConfig
}

var TaskNotRegistered = errors.New("task not registered")

func NewAgent(config AgentConfig) (*Agent, error) {
	id, err := uuid.NewV4()
	if err != nil {
		return nil, err
	}

	return &Agent{
		ID:       id,
		Registry: NewRegistry(),
		Config:   config,
	}, nil
}

func (a *Agent) StartTask(guid, secureToken string, limits TaskLimits) (*Task, error) {
	task, err := a.createTask(secureToken, limits)
	if err != nil {
		return nil, err
	}

	a.Registry.Register(guid, task)

	return task, nil
}

func (a *Agent) StopTask(guid string) error {
	task, present := a.Registry.Lookup(guid)

	if !present {
		return TaskNotRegistered
	}

	err := task.Container.Destroy()
	if err != nil {
		return err
	}

	a.Registry.Unregister(guid)

	return nil
}

func (a *Agent) HandleStarts(mbus cfmessagebus.MessageBus) error {
	return mbus.Subscribe("task.start", func(payload []byte) {
		var start startMessage

		err := json.Unmarshal(payload, &start)
		if err != nil {
			log.Printf("Failed to unmarshal ssh start: %s\n", err)
			return
		}

		go a.handleStart(start)
	})
}

func (a *Agent) HandleStops(mbus cfmessagebus.MessageBus) error {
	return mbus.Subscribe("task.stop", func(payload []byte) {
		var stop stopMessage

		err := json.Unmarshal(payload, &stop)
		if err != nil {
			log.Printf("Failed to unmarshal ssh start: %s\n", err)
			return
		}

		go a.handleStop(stop)
	})
}

func (a *Agent) createTask(secureToken string, limits TaskLimits) (*Task, error) {
	client := warden.NewClient(
		&warden.ConnectionInfo{
			SocketPath: a.Config.WardenSocketPath,
		},
	)

	err := client.Connect()
	if err != nil {
		return nil, err
	}

	container, err := NewWardenContainer(client)
	if err != nil {
		return nil, err
	}

	return NewTask(container, limits, secureToken, taskCommand(container))
}

type startMessage struct {
	Task                   string `json:"task"`
	SecureToken            string `json:"secure_token"`
	MemoryLimitInMegabytes uint64 `json:"memory_limit"`
	DiskLimitInMegabytes   uint64 `json:"disk_limit"`
}

type stopMessage struct {
	Task string `json:"task"`
}

func (a *Agent) handleStart(start startMessage) {
	log.Printf(
		"creating task %s\n",
		start.Task,
	)

	limits := TaskLimits{
		MemoryLimitInBytes: start.MemoryLimitInMegabytes * 1024 * 1024,
		DiskLimitInBytes:   start.DiskLimitInMegabytes * 1024 * 1024,
	}

	_, err := a.StartTask(start.Task, start.SecureToken, limits)
	if err != nil {
		log.Printf("Failed to create task: %s\n", err)
		return
	}
}

func (a *Agent) handleStop(stop stopMessage) {
	log.Printf(
		"stopping task %s\n",
		stop.Task,
	)

	err := a.StopTask(stop.Task)
	if err != nil {
		log.Printf("Failed to stop task: %s\n", err)
		return
	}
}

func taskCommand(container Container) *exec.Cmd {
	wshdSocket := fmt.Sprintf(
		"/opt/warden/containers/%s/run/wshd.sock",
		container.ID(),
	)

	return exec.Command(
		"sudo",
		"/opt/warden/warden/root/linux/skeleton/bin/wsh",
		"--socket", wshdSocket,
		"--user", "vcap",
	)
}
