// Loads seed/*.json (Extended JSON) into the app database and creates the
// read-only user money-checks connects as. Runs once, from docker-entrypoint-initdb.d.
// A string like "now-3h" or "now+480h" becomes a date that far from now: the
// lifecycle recipes look at age, so their documents cannot have fixed dates.
const fs = require("fs");
const app = db.getSiblingDB("app");
const units = { m: 60e3, h: 3600e3 };
function fromNow(v) {
  if (typeof v === "string") {
    const m = /^now([+-])(\d+)([mh])$/.exec(v);
    return m ? new Date(Date.now() + (m[1] === "+" ? 1 : -1) * m[2] * units[m[3]]) : v;
  }
  if (Array.isArray(v)) return v.map(fromNow);
  // Documents, not BSON values: Decimal128 and the rest carry _bsontype.
  if (v && typeof v === "object" && !v._bsontype && !(v instanceof Date)) {
    for (const k of Object.keys(v)) v[k] = fromNow(v[k]);
  }
  return v;
}
for (const file of fs.readdirSync("/seed").filter((f) => f.endsWith(".json")).sort()) {
  const docs = EJSON.parse(fs.readFileSync("/seed/" + file, "utf8"), { relaxed: false });
  app.getCollection(file.replace(/\.json$/, "")).insertMany(docs.map(fromNow));
}
app.createUser({ user: "checks", pwd: "checks", roles: [{ role: "read", db: "app" }] });
