package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/benbjohnson/litestream"
	"github.com/sawka/dashborg-go-sdk/pkg/dash"
)

const PANEL_HTML = `
<panel>
  <h1>Litestream Admin</h1>
  <d-data query="/get-databases" bindvalue="$.data.databases"/>
  <div class="row" style="xcenter;">
    <div>Select Database</div>
    <d-select onchangehandler="/select-db" onchangehandlerdata="@value" bind="$.data.databases" bindvalue="$.state.database"/>
    <d-button if="*$.state.database" handler="/select-db" handlerdata="$.state.database">Refresh</d-button>
  </div>
  <d-error title="DB Error" bind="$.data.errors" class="compact" style="min-width: 300px"/>
  <d-message if="*$.data.success" title="Restore Success" class="success compact" style="min-width: 300px;">
    <div class="col">
      <div><d-text bind="$.data.success"/></div>
      <d-button handler="/clear-success">Close</d-button>
    </div>
  </d-message>
  <div class="col" if="*$.data.generations">
  <h2>Generations</h2>
  <d-table bind="$.data.generations">
    <d-col label="Name">
      <d-text bind=".name"/>
    </d-col>
    <d-col label="Generation">
      <d-text bind=".generation"/>
    </d-col>
    <d-col label="Lag">
      <d-moment duration bind=".lag"/>
    </d-col>
    <d-col label="Created At">
      <d-moment bind=".createdat" mode="s" format="YYYY-MM-DD HH:mma"/>
    </d-col>
    <d-col label="Updated At">
      <d-moment bind=".updatedat" mode="s" format="YYYY-MM-DD HH:mma"/>
    </d-col>
    <d-col label="Restore">
      <d-button handler="/restore-db" handlerdata="$.state.database,.generation">Restore</d-button>
    </d-col>
  </d-table>
  </div>
  <div class="col" if="*$.data.snapshots">
  <h2>Snapshots</h2>
  <d-table bind="$.data.snapshots">
    <d-col label="Replica">
      <d-text bind=".Replica"/>
    </d-col>
    <d-col label="Generation">
      <d-text bind=".Generation"/>
    </d-col>
    <d-col label="Index">
      <d-text bind=".Index"/>
    </d-col>
    <d-col label="Size">
      <d-text bind=".Size"/>
    </d-col>
    <d-col label="CreatedAt">
      <d-moment bind=".CreatedAt" mode="parse" format="YYYY-MM-DD HH:mma"/>
    </d-col>
  </d-table>
  </div>
</panel>
`

type DashborgCommand struct{}

func (c *DashborgCommand) Run(ctx context.Context, args []string) (err error) {
	var configPath string
	fs := flag.NewFlagSet("litestream-databases", flag.ContinueOnError)
	registerConfigFlag(fs, &configPath)
	var panelPassword string
	fs.StringVar(&panelPassword, "password", "", "panel password")
	fs.Usage = c.Usage
	if err := fs.Parse(args); err != nil {
		return err
	} else if fs.NArg() != 0 {
		return fmt.Errorf("too many argument")
	}

	// Load configuration.
	if configPath == "" {
		return errors.New("-config required")
	}
	config, err := ReadConfigFile(configPath)
	if err != nil {
		return err
	}
	cfg := &dash.Config{ProcName: "demo2", AnonAcc: true, AutoKeygen: true}
	dash.StartProcClient(cfg)
	defer dash.WaitForClear()
	dash.RegisterPanelHandler("litestream", "/", func(req *dash.PanelRequest) error {
		if panelPassword != "" {
			ok := req.CheckAuth(dash.AuthPassword{Password: panelPassword})
			if !ok {
				return nil
			}
		}
		req.SetHtml(PANEL_HTML)
		return nil
	})
	dash.RegisterDataHandler("litestream", "/get-databases", func(req *dash.PanelRequest) (interface{}, error) {
		var dbs []string
		for _, dbConfig := range config.DBs {
			db, err := newDBFromConfig(&config, dbConfig)
			if err != nil {
				continue
			}
			dbs = append(dbs, db.Path())
		}
		return dbs, nil
	})
	dash.RegisterPanelHandler("litestream", "/select-db", func(req *dash.PanelRequest) error {
		req.SetData("$.data.errors", nil)
		req.SetData("$.data.generations", nil)
		req.SetData("$.data.snapshots", nil)
		req.SetData("$.data.success", nil)
		if req.Data == nil {
			req.SetData("$.data.errors", "Database Not Specified to /select-db")
		}
		dbPath := req.Data.(string)
		var db *litestream.DB
		for _, dbConfig := range config.DBs {
			dbTest, err := newDBFromConfig(&config, dbConfig)
			if err != nil {
				continue
			}
			if dbTest.Path() == dbPath {
				db = dbTest
				break
			}
		}
		if db == nil {
			req.SetData("$.data.errors", fmt.Sprintf("Database %v Not Found", dbPath))
			return nil
		}
		replicas := db.Replicas
		updatedAt := time.Now()
		genData := make([]interface{}, 0)
		for _, r := range replicas {
			generations, err := r.Generations(ctx)
			if err != nil {
				continue
			}
			for _, gen := range generations {
				stats, err := r.GenerationStats(ctx, gen)
				if err != nil {
					continue
				}
				out := make(map[string]interface{})
				out["name"] = r.Name()
				out["generation"] = gen
				out["lag"] = updatedAt.Sub(stats.UpdatedAt) / time.Millisecond
				out["createdat"] = stats.CreatedAt.Unix()
				out["updatedat"] = stats.UpdatedAt.Unix()
				genData = append(genData, out)
			}
		}
		req.SetData("$.data.generations", genData)

		snapInfos, _ := db.Snapshots(ctx)
		req.SetData("$.data.snapshots", snapInfos)
		return nil
	})
	dash.RegisterPanelHandler("litestream", "/restore-db", func(req *dash.PanelRequest) error {
		req.SetData("$.data.errors", nil)
		req.SetData("$.data.success", nil)
		if req.Data == nil {
			req.SetData("$.data.errors", "Invalid params passed to /restore-db")
			return nil
		}
		argsArr, ok := req.Data.([]interface{})
		if !ok || len(argsArr) != 2 {
			req.SetData("$.data.errors", "Invalid params passed to /restore-db")
			return nil
		}
		dbPath, generation := argsArr[0].(string), argsArr[1].(string)
		c := RestoreCommand{}
		opt := litestream.NewRestoreOptions()
		opt.Verbose = true
		opt.Generation = generation
		r, err := c.loadFromConfig(ctx, dbPath, configPath, &opt)
		if err != nil {
			req.SetData("$.data.errors", err.Error())
			return nil
		}
		if opt.Generation == "" {
			req.SetData("$.data.errors", "No matching backups found")
			return nil
		}
		err = litestream.RestoreReplica(ctx, r, opt)
		if err != nil {
			req.SetData("$.data.errors", err.Error())
			return nil
		}
		req.SetData("$.data.success", fmt.Sprintf("Restored %v generation %v", dbPath, generation))
		return nil
	})
	dash.RegisterPanelHandler("litestream", "/clear-success", func(req *dash.PanelRequest) error {
		req.SetData("$.data.success", nil)
		return nil
	})

	select {}

	return nil
}

// Usage prints the help screen to STDOUT.
func (c *DashborgCommand) Usage() {
	fmt.Printf(`
The dashborg command runs the Litestream Dashborg GUI.

Usage:

	litestream dashborg [arguments]

Arguments:

	-config PATH
	    Specifies the configuration file.
	    Defaults to %s

    -password [pw]
        Panel Password (optional)

`[1:],
		DefaultConfigPath(),
	)
}
