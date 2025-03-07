/*
Copyright 2022 Huawei Cloud Computing Technologies Co., Ltd.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

 http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package app

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/openGemini/openGemini/lib/logger"
	"go.uber.org/zap"
)

const METALOGO = `
 _________   ______   ____    ____  ________  _________     _       
|  _   _  |.' ____ \ |_   \  /   _||_   __  ||  _   _  |   / \      
|_/ | | \_|| (___ \_|  |   \/   |    | |_ \_||_/ | | \_|  / _ \     
    | |     _.____.    | |\  /| |    |  _| _     | |     / ___ \
   _| |_   | \____) | _| |_\/_| |_  _| |__/ |   _| |_  _/ /   \ \_
  |_____|   \______.'|_____||_____||________|  |_____||____| |____|
	
`

const SQLLOGO = `
 _________   ______    ______     ___    _____     
|  _   _  |.' ____ \ .' ____ \  .'   '.  |_   _|
|_/ | | \_|| (___ \_|| (___ \_|/  .-.  \   | |
    | |     _.____.  _.____.   | |   | |   | |   _
   _| |_   | \____) || \____) |\   -'  \_ _| |__/ |
  |_____|   \______.' \______.' \__.\__| |________|

`

const STORELOGO = `
 _________   ______    ______   _________    ___   _______     ________  
|  _   _  |.' ____ \ .' ____ \ |  _   _  | .'   '.|_   __ \   |_   __  |
|_/ | | \_|| (___ \_|| (___ \_||_/ | | \_|/  .-.  \ | |__) |    | |_ \_|
    | |     _.____'.  _.____'.     | |    | |   | | |  __ /     |  _| _
   _| |_   | \____) || \____) |   _| |_   \  '-'  /_| |  \ \_  _| |__/ |
  |_____|   \______.' \______.'  |_____|   '.___.'|____| |___||________|


`

const TSSERVER = `
 _________   ______    ______   ________  _______  ____   ____  ________  _______     
|  _   _  |.' ____ \ .' ____ \ |_   __  ||_   __ \|_  _| |_  _||_   __  ||_   __ \    
|_/ | | \_|| (___ \_|| (___ \_|  | |_ \_|  | |__) | \ \   / /    | |_ \_|  | |__) |   
    | |     _.____''.  _.____''.   |  _| _   |  _/   \ \ / /     |  _| _   |  __ /
   _| |_   | \____) || \____) | _| |__/ | _| |  \ \_  \ ' /     _| |__/ | _| |  \ \_
  |_____|   \______.' \______.'|________||____| |___|  \_/     |________||____| |___|

`

const MONITORLOGO = `
 _________   ______   ____    ____   ___   ____  _____  _____  _________    ___   _______     
|  _   _  |.' ____ \ |_   \  /   _|.'   '.|_   \|_   _||_   _||  _   _  | .'   '.|_   __ \    
|_/ | | \_|| (___ \_|  |   \/   | /  .-.  \ |   \ | |    | |  |_/ | | \_|/  .-.  \ | |__) |
    | |     _.____'.   | |\  /| | | |   | | | |\ \| |    | |      | |    | |   | | |  __ /    
   _| |_   | \____) | _| |_\/_| |_\  '-'  /_| |_\   |_  _| |_    _| |_   \  '-'  /_| |  \ \_
  |_____|   \______.'|_____||_____|'.___.'|_____|\____||_____|  |_____|   '.___.'|____| |___|
`

const MetaUsage = `Runs the TSMeta server.

Usage: ts-meta run [flags]

    -config <path>
            Set the path to the configuration file.
            This defaults to the environment variable META_CONFIG_PATH,
            ~/.ts/meta.conf, or /etc/ts/meta.conf if a file
            is present at any of these locations.
            Disable the automatic loading of a configuration file using
            the null device (such as /dev/null).
    -pidfile <path>
            Write process ID to a file.
    -cpuprofile <path>
            Write CPU profiling information to a file.
    -memprofile <path>
            Write memory usage information to a file.
`

const SqlUsage = `Runs the TSSQL server.

Usage: ts-sql run [flags]

    -config <path>
            Set the path to the configuration file.
            This defaults to the environment variable TSSQL_CONFIG_PATH,
            ~/.ts/tssql.conf, or /etc/ts/tssql.conf if a file
            is present at any of these locations.
            Disable the automatic loading of a configuration file using
            the null device (such as /dev/null).
    -pidfile <path>
            Write process ID to a file.
    -cpuprofile <path>
            Write CPU profiling information to a file.
    -memprofile <path>
            Write memory usage information to a file.
`

const StoreUsage = `Runs the TSStore server.

Usage: ts-store run [flags]

    -config <path>
            Set the path to the configuration file.
            This defaults to the environment variable STORE_CONFIG_PATH,
            ~/.ts/store.conf, or /etc/ts/store.conf if a file
            is present at any of these locations.
            Disable the automatic loading of a configuration file using
            the null device (such as /dev/null).
    -pidfile <path>
            Write process ID to a file.
    -cpuprofile <path>
            Write CPU profiling information to a file.
    -memprofile <path>
            Write memory usage information to a file.
`

const MonitorUsage = `Runs the TSMonitor server.

Usage: ts-monitor run [flags]

    -config <path>
            Set the path to the configuration file.
            This defaults to the environment variable MONITOR_CONFIG_PATH,
            ~/.ts/monitor.conf, or /etc/ts/monitor.conf if a file
            is present at any of these locations.
            Disable the automatic loading of a configuration file using
            the null device (such as /dev/null).
    -pidfile <path>
            Write process ID to a file.
    -cpuprofile <path>
            Write CPU profiling information to a file.
    -memprofile <path>
            Write memory usage information to a file.
`

// BuildInfo represents the build details for the server code.
type BuildInfo struct {
	Version string
	Commit  string
	Branch  string
	Time    string
}

// Options represents the command line options that can be parsed.
type Options struct {
	SpdyConfigPath string
	ConfigPath     string
	PIDFile        string
	Join           string
	Hostname       string
}

func (opt *Options) GetConfigPath() string {
	if opt.ConfigPath != "" {
		return opt.ConfigPath
	}

	return ""
}

func (opt *Options) GetSpdyConfigPath() string {
	return opt.SpdyConfigPath
}

func ParseFlags(usage func(), args ...string) (Options, error) {
	var options Options
	fs := flag.NewFlagSet("", flag.ExitOnError)
	fs.StringVar(&options.ConfigPath, "config", "", "")
	if err := fs.Parse(args); err != nil {
		return Options{}, err
	}
	return options, nil
}

func RemovePIDFile(pidfile string) {
	if pidfile == "" {
		return
	}

	if err := os.Remove(pidfile); err != nil {
		logger.GetLogger().Error("Remove pidfile failed", zap.Error(err))
	}
}

func WritePIDFile(pidfile string) error {
	if pidfile == "" {
		return nil
	}

	pidDir := filepath.Dir(pidfile)
	if err := os.MkdirAll(pidDir, 0700); err != nil {
		return fmt.Errorf("os.MkdirAll failed, error: %s", err)
	}

	pid := strconv.Itoa(os.Getpid())
	if err := ioutil.WriteFile(pidfile, []byte(pid), 0600); err != nil {
		return fmt.Errorf("write pid file failed, error: %s", err)
	}

	return nil
}

func hasSensitiveWord(url string) bool {
	url = strings.ToLower(url)
	sensitiveInfo := "password"
	return strings.Contains(url, sensitiveInfo)
}

func HideQueryPassword(url string) string {
	if !hasSensitiveWord(url) {
		return url
	}
	var buf strings.Builder

	create := "with password"
	url = strings.ToLower(url)
	if strings.Contains(url, create) {
		fields := strings.Fields(url)
		for i, s := range fields {
			if s == "password" {
				buf.WriteString(strings.Join(fields[:i+1], " "))
				buf.WriteString(" [REDACTED] ")
				if i < len(fields)-2 {
					buf.WriteString(strings.Join(fields[i+2:], " "))
				}
				return buf.String()
			}
		}
	}
	set := "set password"
	if strings.Contains(url, set) {
		fields := strings.SplitAfter(url, "=")
		buf.WriteString(fields[0])
		buf.WriteString(" [REDACTED]")
		return buf.String()
	}
	return url
}
