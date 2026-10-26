// Loads seed/*.json (Extended JSON) into the app database and creates the
// read-only user money-checks connects as. Runs once, from docker-entrypoint-initdb.d.
const fs = require("fs");
const app = db.getSiblingDB("app");
for (const file of fs.readdirSync("/seed").filter((f) => f.endsWith(".json")).sort()) {
  const docs = EJSON.parse(fs.readFileSync("/seed/" + file, "utf8"), { relaxed: false });
  app.getCollection(file.replace(/\.json$/, "")).insertMany(docs);
}
app.createUser({ user: "checks", pwd: "checks", roles: [{ role: "read", db: "app" }] });
