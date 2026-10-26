-- The role money-checks connects as: it can only read.
create role checks login password 'checks';
grant usage on schema public to checks;
grant select on all tables in schema public to checks;
