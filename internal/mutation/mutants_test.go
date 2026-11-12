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
			File: "internal/server/lifecycle.go",
			Old:  "\tcase serveErr = <-served:\n",
			New:  "\tcase serveErr = <-served:\n\t\tif serveErr != nil {\n\t\t\treturn serveErr\n\t\t}\n",
		}},
		Kills: []string{"TestServeDrainsTheHubWhenTheListenerFails", "TestSuperviseDrainsOnEveryExit"},
	},
	{
		Name:   "Run returns the listen error itself, without draining",
		Origin: "84f999c gave Run its own early return on a failed listen; with one exit path it would skip the drain",
		Edits: []edit{{
			File: "internal/server/service.go",
			Old:  "\taddr := s.httpServer.Addr\n",
			New: "\taddr := s.httpServer.Addr\n\tif probe, err := net.Listen(\"tcp\", addr); err != nil {\n" +
				"\t\treturn fmt.Errorf(\"http server: %w\", err)\n\t} else {\n\t\t_ = probe.Close()\n\t}\n",
		}},
		Kills: []string{"TestRunDrainsTheServiceWhenItCannotListen"},
	},
	{
		Name:   "the HTTP server is left out of the drain",
		Origin: "0c8494d drained only the hub on a listener failure; d3a9bfe fixed it",
		Edits: []edit{{
			File: "internal/server/service.go",
			Old:  "\t\ts.stopAccepting,\n",
			New:  "",
		}},
		Kills: []string{"TestServeClosesOpenConnectionsWhenTheListenerFails"},
	},
	{
		Name:   "a listener failure that races cancellation is dropped",
		Origin: "061ffa9 introduced it; f23bbde fixed it",
		Edits: []edit{{
			File: "internal/server/lifecycle.go",
			Old:  "\t\tserveErr = <-served\n",
			New:  "\t\t<-served\n",
		}},
		Kills: []string{"TestServeReportsAListenerFailureThatRacesCancellation", "TestSuperviseDrainsOnEveryExit"},
	},
	{
		Name:   "supervise returns before the serving goroutine has finished",
		Origin: "a363d8c carried it into Service.Run; 061ffa9 fixed it",
		Edits: []edit{{
			File: "internal/server/lifecycle.go",
			Old:  "\t\tserveErr = <-served\n",
			New:  "",
		}},
		Kills: []string{"TestSuperviseDrainsOnEveryExit"},
	},
	{
		Name:   "every drain stage gets the whole budget",
		Origin: "8d84659 gave each stage its own half; this test was added with supervise",
		Edits: []edit{{
			File: "internal/server/lifecycle.go",
			Old:  "share := budget / time.Duration(len(stages))",
			New:  "share := budget",
		}},
		Kills: []string{"TestSuperviseSplitsTheBudgetBetweenStages"},
	},
	{
		Name:   "the hub is drained before the HTTP server",
		Origin: "no commit; swapping the stages passed every unit test when the reshape was prototyped",
		Edits: []edit{{
			File: "internal/server/service.go",
			Old:  "\t\ts.stopAccepting,\n\t\ts.hub.shutdown,\n",
			New:  "\t\ts.hub.shutdown,\n\t\ts.stopAccepting,\n",
		}},
		Kills: []string{"TestServeStopsAcceptingBeforeItStopsTheHub"},
	},
	{
		Name:   "each broadcast copies the client set before the fan-out",
		Origin: "8d84659 removed the per-message client snapshot from the broadcast path",
		Edits: []edit{{
			File: "internal/server/hub.go",
			Old:  "\tfor client, inbox := range r.clients {\n",
			New: "\tsnapshot := make([]clientConn, 0, len(r.clients))\n\tfor c := range r.clients {\n" +
				"\t\tsnapshot = append(snapshot, c)\n\t}\n\tfor _, client := range snapshot {\n\t\tinbox := r.clients[client]\n",
		}},
		Kills: []string{"TestHubBroadcastDoesNotAllocate"},
	},
	{
		Name:   "every message takes the JSON round trip",
		Origin: "8d84659 added the canonical fast path; without it a canonical message allocates 7 times",
		Edits: []edit{{
			File: "internal/server/message_json.go",
			Old:  "\tif isCanonicalMessage(raw) {\n",
			New:  "\tif false && isCanonicalMessage(raw) {\n",
		}},
		Kills: []string{"TestNormalizeMessageAllocatesOnlyThePayload"},
	},
	{
		Name:   "every origin check parses a URL",
		Origin: "8d84659 added the canonical-origin fast path, taking the check from 2 allocations to 0",
		Edits: []edit{{
			File: "internal/server/origin.go",
			Old:  "\tif _, exists := p.origins[origin]; exists {\n",
			New:  "\tif _, exists := p.origins[origin]; false && exists {\n",
		}},
		Kills: []string{"TestOriginCheckDoesNotAllocate"},
	},
	{
		Name:   "the rate limiter logs every check without the debugEnabled guard",
		Origin: "no commit; the per-message log AGENTS.md's logging rule forbids, replayed on the limiter",
		Edits: []edit{{
			File: "internal/server/rate_limiter.go",
			Old:  "\treturn rl.allowAt(time.Now())\n",
			New:  "\tok := rl.allowAt(time.Now())\n\tlog().Debug(\"rate limit check\", \"allowed\", ok, \"tokens\", rl.tokens)\n\treturn ok\n",
		}},
		Kills: []string{"TestRateLimiterAllowDoesNotAllocate"},
	},
}
