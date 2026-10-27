-- The user money-checks connects as: it can only read. money-checks refuses to
-- start as a user with a privilege that writes; see docs/adr/0008-mysql-read-only.md.
create user 'checks'@'%' identified by 'checks';
grant select on app.* to 'checks'@'%';
