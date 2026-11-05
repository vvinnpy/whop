package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	shlex "github.com/anmitsu/go-shlex"

	sshconn "whop/internal/connector/ssh"
	"whop/internal/sshconfig"
)

func defaultZmodemOptions() *sshconn.ZmodemOptions {
	options := &sshconn.ZmodemOptions{
		UploadFiles: promptZmodemFiles,
		DownloadDir: func(host sshconfig.Host) string {
			return defaultDownloadDir(host.Alias)
		},
		Progress: os.Stderr,
	}
	if envBool("WHOP_ZMODEM_DEBUG") {
		options.Debug = os.Stderr
	}
	return options
}

func promptZmodemFiles(host sshconfig.Host, stdin io.Reader, stderr io.Writer) ([]string, error) {
	fmt.Fprintf(stderr, "\r\n%s: local file(s) to upload (space separated, blank cancels): ", host.Alias)
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return nil, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, nil
	}
	fields, err := shlex.Split(line, true)
	if err != nil {
		return nil, fmt.Errorf("parse local file list: %w", err)
	}
	files := make([]string, 0, len(fields))
	for _, value := range fields {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		files = append(files, resolvePath(value))
	}
	return files, nil
}

func configureAutoZmodem(runner *sshconn.Runner) {
	if runner == nil {
		return
	}
	runner.Zmodem = defaultZmodemOptions()
}
