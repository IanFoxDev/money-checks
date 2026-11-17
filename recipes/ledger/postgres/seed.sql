-- One ledger with a planted bug for each recipe and rows next to them that must
-- pass. tests.yaml lists what each recipe finds. Transactions are dated three
-- hours ago, inside the window of ledger_unbalanced_transaction.

insert into ledger_accounts (code, type, currency, allow_negative) values
  ('cash:psp', 'asset', 'USD', false),
  ('user:1', 'liability', 'USD', false),
  ('user:2', 'liability', 'USD', false),
  ('user:3', 'liability', 'USD', false),
  ('revenue', 'revenue', 'USD', false),
  ('world', 'equity', 'USD', true),
  ('user:1:credits@lot:20260101T000000Z:aaaaaaaaaaaa', 'liability', 'USD', false),
  ('user:1:credits@lot:29991231T000000Z:bbbbbbbbbbbb', 'liability', 'USD', false),
  ('user:1:credits@lot:20260102T000000Z:cccccccccccc', 'liability', 'USD', false);

insert into ledger_transactions (id, idempotency_key, hash, type, meta, created_at) overriding system value values (1, 'psp:pay_1:captured', '0000000000000000000000000000000000000000000000000000000000000000', 'deposit', '{}', now() at time zone 'utc' - interval '3 hours');
insert into ledger_postings (account, sequence, transaction_id, position, side, amount, balance_after) values
  ('cash:psp', 1, 1, 1, 'D', 10000, 10000),
  ('user:1', 1, 1, 2, 'C', 10000, 10000);
insert into ledger_transactions (id, idempotency_key, hash, type, meta, created_at) overriding system value values (2, 'psp:pay_2:captured', '0000000000000000000000000000000000000000000000000000000000000000', 'deposit', '{}', now() at time zone 'utc' - interval '3 hours');
insert into ledger_postings (account, sequence, transaction_id, position, side, amount, balance_after) values
  ('cash:psp', 2, 2, 1, 'D', 5000, 15000),
  ('user:2', 1, 2, 2, 'C', 5000, 5000);
-- 3: unbalanced: 20.00 in, 15.00 to the user
insert into ledger_transactions (id, idempotency_key, hash, type, meta, created_at) overriding system value values (3, 'psp:pay_3:captured', '0000000000000000000000000000000000000000000000000000000000000000', 'deposit', '{}', now() at time zone 'utc' - interval '3 hours');
insert into ledger_postings (account, sequence, transaction_id, position, side, amount, balance_after) values
  ('cash:psp', 3, 3, 1, 'D', 2000, 17000),
  ('user:3', 1, 3, 2, 'C', 1500, 1500);
-- 4: balance_after edited: 9500 where 10000 - 1000 is 9000
insert into ledger_transactions (id, idempotency_key, hash, type, meta, created_at) overriding system value values (4, 'order:1', '0000000000000000000000000000000000000000000000000000000000000000', 'transfer', '{}', now() at time zone 'utc' - interval '3 hours');
insert into ledger_postings (account, sequence, transaction_id, position, side, amount, balance_after) values
  ('revenue', 1, 4, 1, 'C', 1000, 1000),
  ('user:1', 2, 4, 2, 'D', 1000, 9500);
-- 5: the next posting adds up from the edited balance
insert into ledger_transactions (id, idempotency_key, hash, type, meta, created_at) overriding system value values (5, 'order:2', '0000000000000000000000000000000000000000000000000000000000000000', 'transfer', '{}', now() at time zone 'utc' - interval '3 hours');
insert into ledger_postings (account, sequence, transaction_id, position, side, amount, balance_after) values
  ('revenue', 2, 5, 1, 'C', 500, 1500),
  ('user:1', 3, 5, 2, 'D', 500, 9000);
-- 6: the posting user:2 #2 (D 200, balance 4800) was deleted
insert into ledger_transactions (id, idempotency_key, hash, type, meta, created_at) overriding system value values (6, 'order:3', '0000000000000000000000000000000000000000000000000000000000000000', 'transfer', '{}', now() at time zone 'utc' - interval '3 hours');
insert into ledger_postings (account, sequence, transaction_id, position, side, amount, balance_after) values
  ('revenue', 3, 6, 1, 'C', 200, 1700);
-- 7: user:2 #3 follows #2, which is gone
insert into ledger_transactions (id, idempotency_key, hash, type, meta, created_at) overriding system value values (7, 'order:4', '0000000000000000000000000000000000000000000000000000000000000000', 'transfer', '{}', now() at time zone 'utc' - interval '3 hours');
insert into ledger_postings (account, sequence, transaction_id, position, side, amount, balance_after) values
  ('revenue', 4, 7, 1, 'C', 300, 2000),
  ('user:2', 3, 7, 2, 'D', 300, 4500);
-- 8: user:3 may not go negative and does
insert into ledger_transactions (id, idempotency_key, hash, type, meta, created_at) overriding system value values (8, 'order:5', '0000000000000000000000000000000000000000000000000000000000000000', 'transfer', '{}', now() at time zone 'utc' - interval '3 hours');
insert into ledger_postings (account, sequence, transaction_id, position, side, amount, balance_after) values
  ('revenue', 5, 8, 1, 'C', 2000, 4000),
  ('user:3', 2, 8, 2, 'D', 2000, -500);
-- 9: a lot that expired in January and still holds 30.00
insert into ledger_transactions (id, idempotency_key, hash, type, meta, created_at) overriding system value values (9, 'promo:1', '0000000000000000000000000000000000000000000000000000000000000000', 'credit.grant', '{}', now() at time zone 'utc' - interval '3 hours');
insert into ledger_postings (account, sequence, transaction_id, position, side, amount, balance_after) values
  ('user:1:credits@lot:20260101T000000Z:aaaaaaaaaaaa', 1, 9, 1, 'C', 3000, 3000),
  ('world', 1, 9, 2, 'D', 3000, -3000);
-- 10: world may go negative; this lot expires in 2999
insert into ledger_transactions (id, idempotency_key, hash, type, meta, created_at) overriding system value values (10, 'promo:2', '0000000000000000000000000000000000000000000000000000000000000000', 'credit.grant', '{}', now() at time zone 'utc' - interval '3 hours');
insert into ledger_postings (account, sequence, transaction_id, position, side, amount, balance_after) values
  ('user:1:credits@lot:29991231T000000Z:bbbbbbbbbbbb', 1, 10, 1, 'C', 2000, 2000),
  ('world', 2, 10, 2, 'D', 2000, -5000);
insert into ledger_transactions (id, idempotency_key, hash, type, meta, created_at) overriding system value values (11, 'promo:3', '0000000000000000000000000000000000000000000000000000000000000000', 'credit.grant', '{}', now() at time zone 'utc' - interval '3 hours');
insert into ledger_postings (account, sequence, transaction_id, position, side, amount, balance_after) values
  ('user:1:credits@lot:20260102T000000Z:cccccccccccc', 1, 11, 1, 'C', 1000, 1000),
  ('world', 3, 11, 2, 'D', 1000, -6000);
-- 12: an expired lot that was expired: balance 0
insert into ledger_transactions (id, idempotency_key, hash, type, meta, created_at) overriding system value values (12, 'credits:expire:11', '0000000000000000000000000000000000000000000000000000000000000000', 'credit.expire', '{}', now() at time zone 'utc' - interval '3 hours');
insert into ledger_postings (account, sequence, transaction_id, position, side, amount, balance_after) values
  ('user:1:credits@lot:20260102T000000Z:cccccccccccc', 2, 12, 1, 'D', 1000, 0),
  ('revenue', 6, 12, 2, 'C', 1000, 5000);
