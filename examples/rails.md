# Onboarding a Rails app

A worked example: one Rails + Vite + Sidekiq application made
workspace-ready. Two halves, and the second is the one people underestimate:
the `config.yml` entry (five minutes) and the app-side changes that make the
app *consume* what the workspace provides (the actual onboarding).

Everything here describes a *typical* shape — treat it as a crib, not
gospel. Your app's boot commands and shared resources are what count; a
Claude session with the `claude-workspaces` skill can read your repo and
adapt this for you.

## The config entry

```yaml
values:
  # 10 ports per workspace: 0 = web, 1 = vite; the rest is headroom.
  PORT:
    start: 5000
    per_workspace: 10

env:
  # Global env applies to every project; per-project env below overrides it.
  RAILS_ENV: development

projects:
  myapp:
    repo: ~/dev/myapp
    base_branch: main

    setup:                        # runs at checkout; re-runs when these lines change
      - bundle install
      - npm ci
      # db:prepare, NOT db:setup — setup re-runs whenever this block changes,
      # and db:setup's schema:load would wipe the workspace's data on re-run.
      - bundle exec rails db:prepare

    start:
      - rails:
          command: bundle exec rails server -p ${PORT0}
          description: app server — the UI at http://localhost:${PORT0}
      - vite:
          command: bin/vite dev
          description: frontend assets; needed for any UI work
      - worker:
          command: bundle exec sidekiq
          description: background jobs — mailers, imports

    teardown:                     # on destroy, before the worktree goes
      - bundle exec rails db:drop

    env:
      # EVERYTHING two parallel copies would fight over goes through the
      # workspace's values: ports via ${PORTn}, names via ${WORKSPACE}.
      DATABASE_URL: postgres:///myapp_${WORKSPACE}_development
      PORT: ${PORT0}
      VITE_PORT: ${PORT1}
      REDIS_URL: redis://localhost:6379/0
      SIDEKIQ_REDIS_NAMESPACE: myapp_${WORKSPACE}

    browse_port: ${PORT0}
```

## The app-side checklist

The tool sets the environment; the app must read it. Grep your repo for
every hardcoded port and name — each one is a collision between two
workspaces waiting to happen.

- **Database** — `config/database.yml` must honor the env. Rails does this
  out of the box when `DATABASE_URL` is set, *unless* your database.yml
  hardcodes `database:` names; the safest shape:

  ```yaml
  development:
    <<: *default
    url: <%= ENV["DATABASE_URL"] %>
  ```

  (Or keep named databases but derive them: `database: myapp_<%=
  ENV.fetch("WORKSPACE", "dev") %>_development` — the `WORKSPACE` variable
  is always in the workspace env.)

- **Web server port** — Puma's default `port ENV.fetch("PORT") { 3000 }`
  line (present in the generated `config/puma.rb`) already does the right
  thing; the config above passes `-p ${PORT0}` explicitly as well, which
  wins either way. Just make sure nothing else pins 3000 (procfiles, JS API
  base URLs, OAuth redirect URIs in seeds).

- **Vite** — `config/vite.json` should not pin a port; let it read the env:

  ```json
  { "development": { "port": null } }
  ```

  and set `VITE_RUBY_PORT` (or `VITE_PORT`, depending on your setup) from
  `${PORT1}` in the project `env:` — vite_ruby reads `VITE_RUBY_PORT`
  directly, plain Vite setups usually take `--port $VITE_PORT` on the
  command instead.

- **Redis / Sidekiq** — a shared Redis is fine *if the keyspace is not*:
  either a namespace derived from `${WORKSPACE}` (shown above), a per-
  workspace database number, or per-workspace prefix in your cache config
  (`config.cache_store = :redis_cache_store, { namespace: ENV["WORKSPACE"] }`).

- **Anything else that collides** — file storage paths
  (`config/storage.yml` local service root), webpack-dev-server, mailcatcher
  ports, LiveReload: route each through a `${PORTn}` or a
  `${WORKSPACE}`-derived name, or accept the sharing consciously.

- **Teardown discipline** — whatever `env:` creates per workspace, tear
  down: the `rails db:drop` above reads the same `DATABASE_URL`, so destroy
  removes exactly this workspace's database and nothing else.

## Prove it

```sh
workspace doctor                       # config parses; hints if something's off
workspace new TEST-1 "onboarding trial" myapp
workspace up TEST-1                    # setup runs with live progress, daemons start
workspace browse TEST-1                # dials the port first — refuses if dead
workspace exec TEST-1 myapp env | sort # exactly what the daemons see
workspace destroy TEST-1               # teardown drops the db, dir and allocation go
```

Two of these at once — `TEST-1` and `TEST-2` — is the whole point: different
ports, different databases, one machine, zero collisions.
