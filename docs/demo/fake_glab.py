#!/usr/bin/env python3
"""Stand-in for glab in the demo recording (docs/demo/setup.sh).

Answers `glab api` like a GitLab for the made-up group acme, keeping its
issues in $FAKE_STATE/issues.json so comments, assignments, due dates and
closes made in the recording show up after the next reload. Never opens a
network connection.
"""
import datetime as dt, json, os, re, sys
from urllib.parse import parse_qs, unquote, urlsplit

STATE = os.path.join(os.environ["FAKE_STATE"], "issues.json")
HOST = "gitlab.example.com"
ME = {"id": 1, "username": "demo", "name": "Demo User"}
PEOPLE = [ME] + [{"id": i, "username": u, "name": n} for i, u, n in [
    (2, "amara", "Amara Okafor"), (3, "jonas", "Jonas Weber"), (4, "li.wei", "Li Wei"),
    (5, "sofia", "Sofia Rossi"), (6, "mateo", "Mateo García")]]
PROJECTS = ["acme/platform/api-gateway", "acme/platform/billing-service", "acme/web/dashboard",
            "acme/web/docs-site", "acme/mobile/app"]
now = dt.datetime.now(dt.timezone.utc).replace(microsecond=0)
today = now.date()


def ts(hours_ago):
    return (now - dt.timedelta(hours=hours_ago)).isoformat().replace("+00:00", "Z")


def day(n):
    return None if n is None else (today + dt.timedelta(days=n)).isoformat()


def person(u):
    return next(p for p in PEOPLE if p["username"] == u)


def seed():
    rows = [  # project, title, labels, assignees, due in days, hours since update, notes, description
        (1, "Charges are retried forever after a gateway timeout", ["bug", "priority::high"], ["amara"], -2, 3, 4,
         "When the payment gateway times out, the retry loop never gives up.\n\nSeen in production twice this week."),
        (1, "Add idempotency keys to refunds", ["feature"], [], 5, 30, 1, "Refunds can be sent twice when a client retries."),
        (0, "Rate limiter counts health checks", ["bug"], ["jonas"], 3, 8, 2, "Health checks from the load balancer use up client quotas."),
        (0, "Upgrade TLS settings to the 2026 baseline", ["security"], ["li.wei"], 12, 50, 0, "Drop TLS 1.2 ciphers without forward secrecy."),
        (0, "Route /v2/orders to the new order service", ["feature"], [], None, 70, 3, "Behind a feature flag first."),
        (2, "Revenue chart shows the wrong month in January", ["bug", "frontend"], ["sofia"], 1, 5, 6, "Off-by-one in the month index."),
        (2, "Dark mode", ["feature", "frontend"], ["sofia", "mateo"], 20, 100, 9, "Epic: dark mode for the whole dashboard."),
        (2, "Dark mode: charts and tables", ["frontend"], ["mateo"], 10, 26, 2, "Child of the dark mode issue."),
        (2, "Dark mode: settings toggle", ["frontend"], [], 14, 40, 0, "Child of the dark mode issue."),
        (3, "Document the new webhooks", ["docs"], ["amara"], 6, 12, 1, "Payload examples for every event."),
        (3, "Broken links on the install page", ["docs", "bug"], [], -5, 200, 0, "Three links point at the old domain."),
        (4, "Push notifications arrive twice on Android", ["bug", "mobile"], ["jonas"], 2, 2, 5, "Only on Android 16."),
        (4, "Offline mode for the order list", ["feature", "mobile"], [], None, 300, 2, "Cache the last 50 orders."),
    ]
    issues, notes = [], {}
    for n, (p, title, labels, who, due, age, nn, desc) in enumerate(rows, start=1):
        proj = PROJECTS[p]
        iid = 10 + n
        issues.append({
            "id": 1000 + n, "iid": iid, "project_id": 100 + p, "title": title, "description": desc,
            "state": "opened", "due_date": day(due), "labels": labels,
            "assignees": [person(u) for u in who], "author": PEOPLE[n % len(PEOPLE)],
            "web_url": f"https://{HOST}/{proj}/-/issues/{iid}", "created_at": ts(age + 400),
            "updated_at": ts(age), "user_notes_count": nn, "references": {"full": f"{proj}#{iid}"}})
        notes[str(1000 + n)] = [{"id": 5000 + n * 10 + k, "body": b, "author": PEOPLE[(n + k) % len(PEOPLE)],
                                 "created_at": ts(age + nn - k), "system": False}
                                for k, b in enumerate(["I can reproduce this.", "Looking into it today.",
                                                       "Fix is in review.", "Deployed to staging.",
                                                       "Still seeing it on one node.", "Thanks!"][:nn])]
    return {"issues": issues, "notes": notes, "next": 2000}


def load():
    try:
        return json.load(open(STATE))
    except FileNotFoundError:
        return seed()


def save(db):
    json.dump(db, open(STATE, "w"))


def out(obj):
    print(json.dumps(obj))
    sys.exit(0)


def fail(msg):
    print(json.dumps({"message": msg}), file=sys.stderr)
    sys.exit(1)


args = sys.argv[1:]
if args[:1] != ["api"]:
    fail(f"demo glab: {' '.join(args[:2])} is not part of the demo")
method, path, body = "GET", args[-1], {}
if "--method" in args:
    method = args[args.index("--method") + 1]
if "--input" in args:
    body = json.loads(sys.stdin.read() or "{}")
u = urlsplit(path)
q = {k: v[0] for k, v in parse_qs(u.query).items()}
parts = [unquote(s) for s in u.path.split("/")]
db = load()
by_ref = {(i["references"]["full"].split("#")[0], i["iid"]): i for i in db["issues"]}

if path == "user":
    out(ME)
if parts[0] == "users":
    out([p for p in PEOPLE if p["username"] == q.get("username")])
if path == "graphql":
    nodes = [{"id": f"gid://gitlab/WorkItem/{i['id']}", "widgets": []} for i in db["issues"]]
    for n in nodes:
        if n["id"].endswith(("/1008", "/1009")):
            n["widgets"] = [{"parent": {"id": "gid://gitlab/WorkItem/1007"}}]
    out({"data": {"group": {"workItems": {"pageInfo": {"hasNextPage": False, "endCursor": ""}, "nodes": nodes}}}})
if parts[0] == "todos":
    out([{"id": 1, "action_name": "mentioned", "target_type": "Issue",
          "project": {"path_with_namespace": "acme/platform/billing-service"}, "target": {"iid": 11}}])
if parts[0] == "groups":
    g = parts[1]
    if len(parts) == 2:
        out({"id": 10, "full_path": g, "path": g.split("/")[-1], "name": g})
    if parts[2] == "issues":
        st = q.get("state", "opened")
        out([i for i in db["issues"] if st == "all" or i["state"] == st])
    if parts[2] == "members":
        if len(parts) == 5:
            out({"id": int(parts[4]), "access_level": 30})
        out(PEOPLE)
    if parts[2] == "projects":
        out([{"id": 100 + n, "name": p.split("/")[-1], "path_with_namespace": p,
              "last_activity_at": ts(n)} for n, p in enumerate(PROJECTS)])
if parts[0] == "projects" and len(parts) >= 3:
    proj = parts[1]
    if parts[2] == "issues" and method == "POST" and len(parts) == 3:
        db["next"] += 1
        iid = 100 + db["next"] % 100
        i = {"id": db["next"], "iid": iid, "project_id": 100, "title": body.get("title", ""),
             "description": body.get("description", ""), "state": "opened", "due_date": body.get("due_date"),
             "labels": [], "assignees": [p for p in PEOPLE if p["id"] in body.get("assignee_ids", [])],
             "author": ME, "web_url": f"https://{HOST}/{proj}/-/issues/{iid}", "created_at": ts(0),
             "updated_at": ts(0), "user_notes_count": 0, "references": {"full": f"{proj}#{iid}"}}
        db["issues"].append(i); save(db); out(i)
    i = by_ref.get((proj, int(parts[3]))) if len(parts) > 3 and parts[3].isdigit() else None
    if i is None:
        fail("404 Issue Not Found")
    ns = db["notes"].setdefault(str(i["id"]), [])
    if len(parts) == 5 and parts[4] == "notes":
        if method == "POST":
            db["next"] += 1
            n = {"id": db["next"], "body": body.get("body", ""), "author": ME, "created_at": ts(0), "system": False}
            ns.append(n); i["user_notes_count"] += 1; i["updated_at"] = ts(0); save(db); out(n)
        if q.get("sort") == "desc":
            out(ns[::-1][:1])
        out(ns)
    if method == "PUT":
        if "assignee_ids" in body:
            i["assignees"] = [p for p in PEOPLE if p["id"] in body["assignee_ids"]]
        if "due_date" in body:
            i["due_date"] = body["due_date"] or None
        if "description" in body:
            i["description"] = body["description"]
        if body.get("state_event") == "close":
            i["state"] = "closed"
        if body.get("state_event") == "reopen":
            i["state"] = "opened"
        i["updated_at"] = ts(0); save(db)
    out(i)
out([])
