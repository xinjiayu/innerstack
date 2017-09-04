// Copyright 2015 Authors, All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	_ "expvar"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"

	"github.com/hooto/hlog4g/hlog"
	"github.com/hooto/httpsrv"
	"github.com/lessos/lessgo/crypto/idhash"
	"github.com/lynkdb/kvgo"

	iam_cfg "github.com/hooto/iam/config"
	iam_cli "github.com/hooto/iam/iamclient"
	iam_sto "github.com/hooto/iam/store"
	iam_web "github.com/hooto/iam/websrv/ctrl"
	iam_api "github.com/hooto/iam/websrv/v1"

	lps_cf "code.hooto.com/lessos/lospack/server/config"
	lps_db "code.hooto.com/lessos/lospack/server/data"
	lps_v1 "code.hooto.com/lessos/lospack/websrv/v1"

	los_webui "code.hooto.com/lessos/los-webui"
	los_ws_cp "code.hooto.com/lessos/loscore/websrv/cp"
	los_ws_op "code.hooto.com/lessos/loscore/websrv/ops"
	los_ws_v1 "code.hooto.com/lessos/loscore/websrv/v1"

	los_cf "code.hooto.com/lessos/loscore/config"
	los_db "code.hooto.com/lessos/loscore/data"
	los_host "code.hooto.com/lessos/loscore/hostlet"
	los_api "code.hooto.com/lessos/loscore/losapi"
	los_rpc "code.hooto.com/lessos/loscore/rpcsrv"
	los_sched "code.hooto.com/lessos/loscore/scheduler"
	los_sts "code.hooto.com/lessos/loscore/status"
	los_zm "code.hooto.com/lessos/loscore/zonemaster"

	"code.hooto.com/lessos/los-soho/config"
)

var (
	Version = "0.1.2.dev"
	err     error
)

func main() {

	hs := httpsrv.NewService()

	// initialize configuration
	{
		if err = los_cf.Init(); err != nil {
			log.Fatalf("conf.Initialize error: %s", err.Error())
		}

		if err = config.Init(Version); err != nil {
			log.Fatalf("conf.Initialize error: %s", err.Error())
		}
	}

	// initialize status
	{
		if err = los_sts.Init(); err != nil {
			log.Fatalf("status.Init error: %s", err.Error())
		}
	}

	{
		hlog.Printf("info", "loscore version %s", los_cf.Version)
		hlog.Printf("info", "los-webui version %s", los_webui.Version)
		hlog.Printf("info", "lospack version %s", lps_cf.Version)
		hlog.Printf("info", "los-soho version %s", Version)
	}

	// initialize data/io connection
	{
		// init local cache database
		opts := los_cf.Config.IoConnectors.Options("los_local_cache")
		if opts == nil {
			log.Fatalf("conf.Data No IoConnector (%s) Found", "los_local_cache")
		}

		if los_db.LocalDB, err = kvgo.Open(*opts); err != nil {
			log.Fatalf("Can Not Connect To %s, Error: %s", opts.Name, err.Error())
		}

		// init zone master database
		opts = los_cf.Config.IoConnectors.Options("los_zone_master")
		if opts == nil {
			log.Fatalf("conf.Data No IoConnector (%s) Found", "los_zone_master")
		}

		if los_db.ZoneMaster, err = kvgo.Open(*opts); err != nil {
			log.Fatalf("Can Not Connect To %s, Error: %s", opts.Name, err.Error())
		}

		los_db.HiMaster = los_db.ZoneMaster
	}

	// module/IAM
	{
		//
		iam_cfg.Prefix = los_cf.Prefix + "/vendor/github.com/hooto/iam"
		iam_cfg.Config.InstanceID = idhash.HashToHexString([]byte("los-soho/iam"), 16)

		// init database
		iam_sto.PathPrefixSet("/global/iam")
		iam_sto.Data = los_db.ZoneMaster
		if err := iam_sto.Init(); err != nil {
			log.Fatalf("iam.Store.Init error: %s", err.Error())
		}
		if err := iam_sto.InitData(); err != nil {
			log.Fatalf("iam.Store.InitData error: %s", err.Error())
		}

		//
		iam_cli.ServiceUrl = fmt.Sprintf(
			"http://%s:%d/iam",
			los_cf.Config.Host.LanAddr.IP(),
			los_cf.Config.Host.HttpPort,
		)
		if los_cf.Config.IamServiceUrlFrontend == "" {
			if los_cf.Config.Host.WanAddr.IP() != "" {
				iam_cli.ServiceUrlFrontend = fmt.Sprintf(
					"http://%s:%d/iam",
					los_cf.Config.Host.WanAddr.IP(),
					los_cf.Config.Host.HttpPort,
				)
			} else {
				iam_cli.ServiceUrlFrontend = iam_cli.ServiceUrl
			}
		} else {
			iam_cli.ServiceUrlFrontend = los_cf.Config.IamServiceUrlFrontend
		}
		hlog.Printf("info", "IAM ServiceUrl %s", iam_cli.ServiceUrl)
		hlog.Printf("info", "IAM ServiceUrlFrontend %s", iam_cli.ServiceUrlFrontend)

		//
		if err := iam_cfg.InitConfig(); err != nil {
			log.Fatalf("iam_cfg.InitConfig error: %s", err.Error())
		}
		iam_sto.SysConfigRefresh()

		//
		hs.ModuleRegister("/iam/v1", iam_api.NewModule())
		hs.ModuleRegister("/iam", iam_web.NewModule())
	}

	// module/LPS: init lps database and webserver
	{
		if err = lps_cf.Init(los_cf.Prefix); err != nil {
			log.Fatalf("lps.Config.Init error: %s", err.Error())
		}

		if err = lps_db.Init(lps_cf.Config.IoConnectors); err != nil {
			log.Fatalf("lps.Data.Init error: %s", err.Error())
		}

		if err := iam_sto.AppInstanceRegister(lps_cf.IamAppInstance()); err != nil {
			log.Fatalf("lps.Data.Init error: %s", err.Error())
		}

		hs.ModuleRegister("/lps/v1", lps_v1.NewModule())
		hs.ModuleRegister("/los/cp/lps/~", httpsrv.NewStaticModule("lps_ui", los_cf.Prefix+"/webui/lps"))

		// TODO
		los_cf.Config.LpsServiceUrl = fmt.Sprintf(
			"http://%s:%d/",
			los_cf.Config.Host.LanAddr.IP(),
			los_cf.Config.Host.HttpPort,
		)
	}

	// loscore
	{
		if err := iam_sto.AppInstanceRegister(config.IamAppInstance()); err != nil {
			log.Fatalf("los.Data.Init error: %s", err.Error())
		}

		hs.HandlerFuncRegister("/los/v1/pb/termws", los_ws_v1.PodBoundTerminalWsHandlerFunc)

		// Frontend APIs/UI for Users
		hs.ModuleRegister("/los/v1", los_ws_v1.NewModule())
		hs.ModuleRegister("/los/cp", los_ws_cp.NewModule())
		// Backend Operating APIs/UI for System Operators
		hs.ModuleRegister("/los/ops", los_ws_op.NewModule())

		// i18n
		// hs.Config.I18n(los_cf.Prefix + "/i18n/en.json")
		// hs.Config.I18n(los_cf.Prefix + "/i18n/zh_CN.json")
	}

	// init zonemaster
	{
		if err := los_zm.InitData(config.InitZoneMasterData()); err != nil {
			log.Fatal(err.Error())
		}

		los_zm.Scheduler = los_sched.NewScheduler()
		los_api.RegisterApiZoneMasterServer(los_rpc.Server, new(los_zm.ApiZoneMaster))

		if err := los_zm.Start(); err != nil {
			log.Fatal(err.Error())
		}
	}

	//
	{
		if err := los_host.InitData(config.InitHostletData()); err != nil {
			log.Fatal(err.Error())
		}

		if err := los_host.Start(); err != nil {
			log.Fatal(err.Error())
		}
	}

	//
	if err := los_rpc.Start(los_cf.Config.Host.LanAddr.Port()); err != nil {
		log.Fatalf("rpc.server.Start error: %v", err)
	}

	// http service
	hs.Config.HttpPort = los_cf.Config.Host.HttpPort
	go hs.Start()

	// job/task
	// go nodelet.Start()
	// go scheduler.Start()

	if los_cf.Config.PprofHttpPort > 0 {
		go http.ListenAndServe(fmt.Sprintf(":%d", los_cf.Config.PprofHttpPort), nil)
	}

	los_cf.Config.Sync()

	select {}
}
