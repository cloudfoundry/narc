package narc

import (
	"code.google.com/p/go.crypto/ssh"
	"fmt"
	"github.com/cloudfoundry/go_cfmessagebus/mock_cfmessagebus"
	"github.com/kr/pty"
	"github.com/nu7hatch/gouuid"
	"io"
	. "launchpad.net/gocheck"
	"os/exec"
	"time"
)

type PSSuite struct {
	ProxyServer *ProxyServer

	serverPort int

	registry *Registry

	task   *Task
	taskID string
}

func init() {
	Suite(&PSSuite{})
}

func (s *PSSuite) SetUpTest(c *C) {
	containerProvider := WardenContainerProvider{
		WardenSocketPath: "/tmp/warden.sock",
	}

	agent, err := NewAgent(containerProvider)

	if err != nil {
		panic(err)
	}

	messageBus := mock_cfmessagebus.NewMockMessageBus()

	err = agent.HandleStarts(messageBus)
	c.Assert(err, IsNil)

	err = agent.HandleStops(messageBus)
	c.Assert(err, IsNil)

	taskUUID, err := uuid.NewV4()
	if err != nil {
		panic(err)
	}

	taskToken, err := uuid.NewV4()
	if err != nil {
		panic(err)
	}

	s.taskID = taskUUID.String()

	messageBus.PublishSync("task.start", []byte(fmt.Sprintf(`
	    {"task":"%s","secure_token":"%s","memory_limit":32,"disk_limit":1}
	`, s.taskID, taskToken.String())))

	task, found := agent.Registry.Lookup(s.taskID)
	c.Assert(found, Equals, true)

	s.task = task

	s.ProxyServer, err = NewProxyServer(agent.Registry)
	if err != nil {
		panic(err)
	}

	randomPort, err := grabEphemeralPort()
	if err != nil {
		randomPort = 7331
	}

	s.serverPort = randomPort

	err = s.ProxyServer.Start(randomPort)
	if err != nil {
		panic(err)
	}

	err = waitForPort(randomPort)
	if err != nil {
		panic(err)
	}
}

func (s *PSSuite) TearDownTest(c *C) {
	s.task.Stop()
	s.ProxyServer.Stop()
}

func (s *PSSuite) TestProxyServerForwardsOutput(c *C) {
	_, _, reader := s.connectedTask(c)
	expect(c, reader, fmt.Sprintf(`vcap@%s:~\$`, s.task.container.ID()))
}

func (s *PSSuite) TestProxyServerForwardsInput(c *C) {
	_, writer, reader := s.connectedTask(c)

	expect(c, reader, fmt.Sprintf(`vcap@%s:~\$`, s.task.container.ID()))

	writer.Write([]byte("echo hi\n"))

	expect(c, reader, ` echo hi\r\n`)
	expect(c, reader, `hi\r\n`)
}

func (s *PSSuite) TestProxyServerDestroysContainerWhenProcessEnds(c *C) {
	_, writer, reader := s.connectedTask(c)

	expect(c, reader, fmt.Sprintf(`vcap@%s:~\$`, s.task.container.ID()))

	writer.Write([]byte("exit\n"))

	expect(c, reader, ` exit\r\n`)

	time.Sleep(1 * time.Second)

	_, err := s.task.container.Run("")
	c.Assert(err, NotNil)
}

func (s *PSSuite) TestProxyServerDestroysContainerWhenProcessEndsWhileDetached(c *C) {
	client, writer, reader := s.connectedTask(c)

	expect(c, reader, fmt.Sprintf(`vcap@%s:~\$`, s.task.container.ID()))

	writer.Write([]byte("sleep 1; exit\n"))

	expect(c, reader, ` sleep 1; exit\r\n`)

	client.Process.Kill()

	time.Sleep(2 * time.Second)

	_, err := s.task.container.Run("")
	c.Assert(err, NotNil)
}

func (s *PSSuite) TestProxyServerKeepsContainerOnDisconnect(c *C) {
	_, writer, reader := s.connectedTask(c)

	expect(c, reader, fmt.Sprintf(`vcap@%s:~\$`, s.task.container.ID()))

	writer.Close()

	time.Sleep(1 * time.Second)

	res, err := s.task.container.Run("exit 42")
	c.Assert(err, IsNil)

	c.Assert(res.ExitStatus, Equals, uint32(42))
}

func (s *PSSuite) TestProxyServerAttachesToRunningProcess(c *C) {
	client, writer, reader := s.connectedTask(c)

	expect(c, reader, fmt.Sprintf(`vcap@%s:~\$`, s.task.container.ID()))

	writer.Write([]byte(`ruby -e '
Thread.new do
  while true
    puts "---"
    sleep 0.5
  end
end

while true
  puts gets.upcase
end'`))

	writer.Write([]byte("\n"))

	expect(c, reader, `---\r\n`)

	writer.Write([]byte("hello\n"))
	expect(c, reader, `HELLO\r\n`)

	client.Process.Kill()

	_, writer, reader = s.connectedTask(c)

	expect(c, reader, `---\r\n`)

	writer.Write([]byte("hello again\n"))
	expect(c, reader, `HELLO AGAIN\r\n`)
}

func (s *PSSuite) TestProxyServerRejectsInvalidToken(c *C) {
	config := &ssh.ClientConfig{
		User: s.taskID,
		Auth: []ssh.ClientAuth{
			ssh.ClientAuthPassword(PasswordAuth{"some-bogus-token"}),
		},
	}

	_, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", s.serverPort), config)
	c.Assert(err, NotNil)
}

func (s *PSSuite) TestProxyServerRejectsUnknownTask(c *C) {
	config := &ssh.ClientConfig{
		User: "some-bogus-task-id",
		Auth: []ssh.ClientAuth{
			ssh.ClientAuthPassword(PasswordAuth{s.task.SecureToken}),
		},
	}

	_, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", s.serverPort), config)
	c.Assert(err, NotNil)
}

func (s *PSSuite) TestAgentTaskMemoryLimitsMakesTaskDie(c *C) {
	_, writer, reader := s.connectedTask(c)

	expect(c, reader, fmt.Sprintf(`vcap@%s:~\$`, s.task.container.ID()))

	writer.Write([]byte("ruby -e \"'a'*10*1024*1024\"\n"))

	expect(c, reader, fmt.Sprintf(`vcap@%s:~\$`, s.task.container.ID()))

	writer.Write([]byte("ruby -e \"'a'*33*1024*1024\"\n"))

	expect(c, reader, "Killed")
}

func (s *PSSuite) TestAgentTaskDiskLimitsEnforcesQuota(c *C) {
	_, writer, reader := s.connectedTask(c)

	expect(c, reader, fmt.Sprintf(`vcap@%s:~\$`, s.task.container.ID()))

	writer.Write([]byte("ruby -e \"print('a' * 1024 * 512)\" > foo.txt\n"))

	expect(c, reader, fmt.Sprintf(`vcap@%s:~\$`, s.task.container.ID()))

	writer.Write([]byte("du foo.txt | awk '{print $1}'\n"))

	expect(c, reader, "512\r\n")

	expect(c, reader, fmt.Sprintf(`vcap@%s:~\$`, s.task.container.ID()))

	writer.Write([]byte("ruby -e \"print('a' * 1024 * 1024 * 2)\" > foo.txt\n"))

	expect(c, reader, "Disk quota exceeded")
}

func (s *PSSuite) connectedTask(c *C) (*exec.Cmd, io.WriteCloser, *Expector) {
	sshCmd := exec.Command(
		"ssh",
		"127.0.0.1",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=no",
		"-l", s.taskID,
		"-p", fmt.Sprintf("%d", s.serverPort),
	)

	pty, err := pty.Start(sshCmd)
	c.Assert(err, IsNil)

	// just so there's something sane
	setWinSize(pty, 80, 24)

	reader := NewExpector(pty, 5*time.Second)

	expect(c, reader, "password:")
	pty.Write([]byte(fmt.Sprintf("%s\n", s.task.SecureToken)))

	return sshCmd, pty, reader
}

type PasswordAuth struct {
	password string
}

func (p PasswordAuth) Password(user string) (string, error) {
	return p.password, nil
}
