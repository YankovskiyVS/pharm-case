-- Application code rejects unconfigured doctor MFA. NOT VALID keeps legacy rows
-- readable for operator repair while enforcing the rule on new/updated accounts.
ALTER TABLE users ADD CONSTRAINT doctor_totp_configured CHECK (
 role != 'doctor' OR totp_secret ~ '^[A-Z2-7]{26,128}$'
) NOT VALID;
ALTER TABLE users ADD CONSTRAINT normalized_email CHECK (email=lower(email)) NOT VALID;
