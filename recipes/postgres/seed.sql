-- Rows that break each recipe, and rows next to them that must not.

-- negative_balance: ben and dana are below zero; zero is fine, and so is the
-- house account, which is allowed to go negative.
insert into accounts (id, owner, currency, balance, allow_negative) values
    ('acc_anna',  'anna',  'USD',   25.00, false),
    ('acc_ben',   'ben',   'EUR',   -3.50, false),
    ('acc_carl',  'carl',  'USD',    0.00, false),
    ('acc_dana',  'dana',  'JPY', -500.00, false),
    ('acc_house', 'house', 'USD', -120.00, true);
