package mutation

// mutants is the list of bugs the guard tests in this module must catch. Each
// one is a past mistake, replayed against today's code: the harness applies its
// edits to a copy of the module and runs the tests in Kills, each of which must
// fail. Name the commit that introduced the bug, or the one that added the test
// when the bug was found by hand.
//
// An edit whose Old text no longer appears exactly once fails the harness, so a
// refactor that moves the guarded code has to move its mutant too. To add a
// mutant for a new guard test, see "Guard tests and mutants" in
// docs/guides/testing.md.
var mutants = []mutant{
	{
		Name:   "SERVER_PORT is read but never applied",
		Origin: "the test was added with test/repocheck's env-var cross-check",
		Edits: []edit{{
			File: "internal/server/config.go",
			Old:  "\t\tcfg.Port = port\n",
			New:  "\t\t_ = port\n",
		}},
		Kills: []string{"TestNewConfigFromEnvPort"},
	},
	{
		Name:   "ALLOWED_ORIGINS is taken as one origin, not split on commas",
		Origin: "the test was added with test/repocheck's env-var cross-check",
		Edits: []edit{{
			File: "internal/server/config.go",
			Old:  "\t\tcfg.AllowedOrigins = parseOrigins(origins)\n",
			New:  "\t\tcfg.AllowedOrigins = []string{origins}\n",
		}},
		Kills: []string{"TestNewConfigFromEnvAllowedOrigins"},
	},
	{
		Name:   "LOG_LEVEL is not trimmed before it is parsed",
		Origin: "the test was added with test/repocheck's env-var cross-check",
		Edits: []edit{{
			File: "internal/server/logging.go",
			Old:  "\traw := strings.TrimSpace(os.Getenv(\"LOG_LEVEL\"))\n",
			New:  "\traw := os.Getenv(\"LOG_LEVEL\")\n\t_ = strings.TrimSpace\n",
		}},
		Kills: []string{"TestLogLevelFromEnv"},
	},
	{
		Name:   "resolveConfig ignores a field added to Config",
		Origin: "69d8b2d added a test that passed this mutant; 4bc4afd fixed it",
		Edits: []edit{{
			File: "internal/server/config.go",
			Old:  "\tRateLimit      RateLimitConfig\n}",
			New:  "\tRateLimit      RateLimitConfig\n\tMutantField    int\n}",
		}},
		Kills: []string{"TestEveryConfigFieldReachesTheSettings"},
	},
	{
		Name:   "resolveConfig drops a field Config already has",
		Origin: "69d8b2d removed the field-by-field copy that could do this",
		Edits: []edit{{
			File: "internal/server/config.go",
			Old:  "\t\tmaxMessageSize: raw.MaxMessageSize,\n",
			New:  "",
		}},
		Kills: []string{"TestResolveConfigCarriesValidValuesAcross", "TestEveryConfigFieldReachesTheSettings"},
	},
	{
		Name:   "New listens on the caller's raw port",
		Origin: "ef3cfbb fixed it, and says the raw port reached the server before the branch that found it",
		Edits: []edit{{
			File: "internal/server/service.go",
			Old:  "\treturn newService(resolveConfig(cfg))\n",
			New: "\tsvc := newService(resolveConfig(cfg))\n\tif cfg != nil {\n" +
				"\t\tsvc.httpServer.Addr = cfg.Port\n\t}\n\treturn svc\n",
		}},
		Kills: []string{"TestRawConfigStopsAtTheBoundary", "TestNewListensOnTheResolvedPort"},
	},
	{
		Name:   "a raw Config is threaded past New",
		Origin: "6fa491f embedded the raw Config in the resolved one; bd35c26 then left two allow-lists, which ae709e9 fixed",
		Edits: []edit{
			{
				File: "internal/server/service.go",
				Old:  "func newService(s settings) *Service {\n",
				New:  "func newService(s settings, cfg *Config) *Service {\n\t_ = cfg\n",
			},
			{
				File: "internal/server/service.go",
				Old:  "\treturn newService(resolveConfig(cfg))\n",
				New:  "\treturn newService(resolveConfig(cfg), cfg)\n",
			},
		},
		Kills: []string{"TestRawConfigStopsAtTheBoundary"},
	},
	{
		Name:   "writeFrame re-reads the queue depth on every pass",
		Origin: "269c99f wrote the loop; e2526d2 added the test that kills this mutant",
		Edits: []edit{{
			File: "internal/server/client.go",
			Old:  "\tfor range len(queued) {\n",
			New:  "\tfor len(queued) > 0 {\n",
		}},
		Kills: []string{"TestWriteFrameLeavesLaterMessagesForTheNextFrame"},
	},
	{
		Name:   "a package-level var calls the clock seam",
		Origin: "c36deb0 added TestClockSeamIsTestOnly; a package-level var passed it until 4bc4afd",
		Edits: []edit{{
			File: "internal/server/zz_mutant_probe.go",
			New:  "package server\n\nimport \"time\"\n\nvar mutantProbe = newRateLimiterAt(1, time.Second, time.Now())\n",
		}},
		Kills: []string{"TestClockSeamIsTestOnly"},
	},
	{
		Name:   "allowAt never advances its baseline",
		Origin: "c36deb0 mutation-checked the limiter baseline; this test pins the refill",
		Edits: []edit{{
			File: "internal/server/rate_limiter.go",
			Old:  "\t\trl.last = now\n",
			New:  "",
		}},
		Kills: []string{"TestRateLimiterRefillsFromElapsedTime"},
	},
	{
		Name:   "a listener failure returns without draining",
		Origin: "a363d8c introduced it; 0c8494d fixed it",
		Edits: []edit{{
			File: "internal/server/service.go",
			Old:  "return errors.Join(fmt.Errorf(\"http server: %w\", err), s.shutdown())",
			New:  "return fmt.Errorf(\"http server: %w\", err)",
		}},
		Kills: []string{"TestServeDrainsTheHubWhenTheListenerFails"},
	},
	{
		Name:   "a listener failure drains only the hub",
		Origin: "0c8494d left it; d3a9bfe fixed it",
		Edits: []edit{{
			File: "internal/server/service.go",
			Old:  "return errors.Join(fmt.Errorf(\"http server: %w\", err), s.shutdown())",
			New:  "return errors.Join(fmt.Errorf(\"http server: %w\", err), withStageDeadline(context.Background(), s.hub.shutdown))",
		}},
		Kills: []string{"TestServeClosesOpenConnectionsWhenTheListenerFails"},
	},
	{
		Name:   "a listener failure that races cancellation is dropped",
		Origin: "061ffa9 introduced it; f23bbde fixed it",
		Edits: []edit{{
			File: "internal/server/service.go",
			Old:  "\t\tif serveErr != nil {\n",
			New:  "\t\tif serveErr != nil && false {\n",
		}},
		Kills: []string{"TestServeReportsAListenerFailureThatRacesCancellation"},
	},
}
