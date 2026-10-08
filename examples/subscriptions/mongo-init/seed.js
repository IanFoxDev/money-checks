// Loads seed/mongo/*.json (Extended JSON) into the shop database and creates the
// read-only user money-checks connects as. Runs once, from docker-entrypoint-initdb.d.
const fs = require("fs");
const shop = db.getSiblingDB("shop");
for (const file of fs.readdirSync("/seed").filter((f) => f.endsWith(".json")).sort()) {
  const docs = EJSON.parse(fs.readFileSync("/seed/" + file, "utf8"), { relaxed: false });
  shop.getCollection(file.replace(/\.json$/, "")).insertMany(docs);
}
shop.createUser({ user: "checks", pwd: "checks", roles: [{ role: "read", db: "shop" }] });
