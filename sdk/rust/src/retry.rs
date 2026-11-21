//! Retry policy shared with the other SDKs (see sdk/README.md).

use std::collections::hash_map::RandomState;
use std::hash::{BuildHasher, Hasher};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

/// Retries after the first attempt (so up to 4 attempts).
pub(crate) const DEFAULT_MAX_RETRIES: u32 = 3;
const BASE_BACKOFF_MS: u64 = 250;
const MAX_BACKOFF_MS: u64 = 8000;
/// A longer wait is surfaced to the caller as the error instead of being slept out.
pub(crate) const MAX_RETRY_WAIT: Duration = Duration::from_secs(60);

pub(crate) fn is_idempotent(method: &str) -> bool {
    matches!(method, "GET" | "PUT" | "DELETE")
}

pub(crate) fn is_retryable_status(status: u16) -> bool {
    status == 429 || status == 503
}

/// Exponential backoff with jitter in [d/2, d].
pub(crate) fn backoff(attempt: u32) -> Duration {
    backoff_with(attempt, unit_random())
}

fn backoff_with(attempt: u32, unit: f64) -> Duration {
    let d = (BASE_BACKOFF_MS << attempt.min(10)).min(MAX_BACKOFF_MS) as f64;
    Duration::from_secs_f64((d / 2.0 + unit * (d / 2.0)) / 1000.0)
}

/// A float in [0, 1) without a random-number dependency.
fn unit_random() -> f64 {
    let mut h = RandomState::new().build_hasher();
    h.write_u128(SystemTime::now().duration_since(UNIX_EPOCH).map_or(0, |d| d.as_nanos()));
    (h.finish() >> 11) as f64 / (1u64 << 53) as f64
}

/// Reads delay-seconds or an HTTP date; `None` when absent, zero or unusable.
pub(crate) fn parse_retry_after(value: Option<&str>, now: SystemTime) -> Option<Duration> {
    let v = value?.trim();
    if v.is_empty() {
        return None;
    }
    let wait = if v.bytes().all(|b| b.is_ascii_digit() || b == b'.') {
        let secs: f64 = v.parse().ok().filter(|s: &f64| s.is_finite())?;
        Duration::from_millis((secs * 1000.0).round() as u64)
    } else {
        parse_http_date(v)?.duration_since(now).unwrap_or(Duration::ZERO)
    };
    (!wait.is_zero()).then_some(wait)
}

/// Days since 1970-01-01 of a proleptic Gregorian date (Howard Hinnant's algorithm).
pub(crate) fn days_from_civil(y: i64, m: u32, d: u32) -> i64 {
    let y = if m <= 2 { y - 1 } else { y };
    let era = y.div_euclid(400);
    let yoe = y.rem_euclid(400);
    let mp = i64::from((m + 9) % 12);
    let doy = (153 * mp + 2) / 5 + i64::from(d) - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146_097 + doe - 719_468
}

/// Unix seconds to `SystemTime`; a value the platform cannot represent reads as the epoch.
pub(crate) fn from_unix(secs: i64) -> SystemTime {
    let d = Duration::from_secs(secs.unsigned_abs());
    let t = if secs >= 0 { UNIX_EPOCH.checked_add(d) } else { UNIX_EPOCH.checked_sub(d) };
    t.unwrap_or(UNIX_EPOCH)
}

fn month(name: &str) -> Option<u32> {
    const MONTHS: [&str; 12] = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
    MONTHS.iter().position(|m| m.eq_ignore_ascii_case(name)).map(|i| i as u32 + 1)
}

/// IMF-fixdate, e.g. `Sun, 06 Nov 1994 08:49:37 GMT` (the form RFC 9110 requires senders to use).
fn parse_http_date(v: &str) -> Option<SystemTime> {
    let rest = v.split_once(',').map_or(v, |(_, r)| r);
    let mut parts = rest.split_whitespace();
    let day: u32 = parts.next()?.parse().ok()?;
    let mon = month(parts.next()?)?;
    let year = parts.next().filter(|y| y.len() <= 4)?.parse::<i64>().ok()?;
    let mut hms = parts.next()?.split(':');
    let (h, mi, s): (i64, i64, i64) = (hms.next()?.parse().ok()?, hms.next()?.parse().ok()?, hms.next()?.parse().ok()?);
    if !matches!(parts.next()?, "GMT" | "UTC") || parts.next().is_some() || hms.next().is_some() {
        return None;
    }
    if !(1..=31).contains(&day) || h > 23 || mi > 59 || s > 60 {
        return None;
    }
    Some(from_unix(days_from_civil(year, mon, day) * 86_400 + h * 3600 + mi * 60 + s))
}

/// RFC 3339 timestamp (`2026-11-08T12:10:00Z`, with optional fraction and offset).
pub(crate) fn parse_rfc3339(v: &str) -> Option<SystemTime> {
    let b = v.as_bytes();
    if b.len() < 20
        || !v.is_ascii()
        || b[4] != b'-'
        || b[7] != b'-'
        || !matches!(b[10], b'T' | b't' | b' ')
        || b[13] != b':'
        || b[16] != b':'
    {
        return None;
    }
    let int = |r: std::ops::Range<usize>| -> Option<i64> {
        let s = &v[r];
        if s.bytes().all(|c| c.is_ascii_digit()) {
            s.parse().ok()
        } else {
            None
        }
    };
    let (year, mon, day) = (int(0..4)?, int(5..7)?, int(8..10)?);
    let (h, mi, s) = (int(11..13)?, int(14..16)?, int(17..19)?);
    if !(1..=12).contains(&mon) || !(1..=31).contains(&day) || h > 23 || mi > 59 || s > 60 {
        return None;
    }
    let mut i = 19;
    let mut nanos = 0u32;
    if b[i] == b'.' {
        let start = i + 1;
        i = start;
        while i < b.len() && b[i].is_ascii_digit() {
            i += 1;
        }
        if i == start {
            return None;
        }
        let digits = &v[start..i.min(start + 9)];
        nanos = digits.parse::<u32>().ok()? * 10u32.pow(9 - digits.len() as u32);
    }
    let offset = match b.get(i)? {
        b'Z' | b'z' if i + 1 == b.len() => 0,
        sign @ (b'+' | b'-') if b.len() == i + 6 && b[i + 3] == b':' => {
            let (oh, om) = (int(i + 1..i + 3)?, int(i + 4..i + 6)?);
            if oh > 23 || om > 59 {
                return None;
            }
            let secs = oh * 3600 + om * 60;
            if *sign == b'+' {
                secs
            } else {
                -secs
            }
        }
        _ => return None,
    };
    let unix = days_from_civil(year, mon as u32, day as u32) * 86_400 + h * 3600 + mi * 60 + s - offset;
    from_unix(unix).checked_add(Duration::from_nanos(u64::from(nanos)))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn at(secs: u64) -> SystemTime {
        UNIX_EPOCH + Duration::from_secs(secs)
    }

    #[test]
    fn civil_dates() {
        assert_eq!(days_from_civil(1970, 1, 1), 0);
        assert_eq!(days_from_civil(2024, 2, 29) * 86_400, 1_709_164_800);
        assert_eq!(days_from_civil(1969, 12, 31), -1);
    }

    #[test]
    fn http_date() {
        assert_eq!(parse_http_date("Sun, 06 Nov 1994 08:49:37 GMT"), Some(at(784_111_777)));
        assert_eq!(parse_http_date("Thu, 01 Jan 2099 00:00:00 GMT"), Some(at(4_070_908_800)));
        for bad in [
            "",
            "garbage",
            "Sun, 06 Nov 1994 08:49:37 PST",
            "Sun, 32 Nov 1994 08:49:37 GMT",
            "Sun, 06 Xyz 1994 08:49:37 GMT",
            "Sun, 06 Nov 99999999999999999999 08:49:37 GMT",
            "Sun, 06 Nov -1994 08:49:37 GMT",
        ] {
            assert_eq!(parse_http_date(bad), None, "{bad}");
        }
    }

    #[test]
    fn retry_after() {
        let now = at(784_111_777 - 30);
        assert_eq!(parse_retry_after(Some("7"), now), Some(Duration::from_secs(7)));
        assert_eq!(parse_retry_after(Some(" 1.5 "), now), Some(Duration::from_millis(1500)));
        assert_eq!(parse_retry_after(Some("Sun, 06 Nov 1994 08:49:37 GMT"), now), Some(Duration::from_secs(30)));
        for none in [None, Some(""), Some("0"), Some("soon"), Some("-3")] {
            assert_eq!(parse_retry_after(none, now), None, "{none:?}");
        }
        // An HTTP date in the past is no wait at all.
        assert_eq!(parse_retry_after(Some("Sun, 06 Nov 1994 08:49:37 GMT"), at(784_111_777 + 5)), None);
    }

    #[test]
    fn rfc3339() {
        assert_eq!(parse_rfc3339("2026-11-08T12:10:00Z"), Some(at(1_794_139_800)));
        assert_eq!(parse_rfc3339("2026-11-08T14:10:00+02:00"), Some(at(1_794_139_800)));
        assert_eq!(parse_rfc3339("2026-11-08T07:40:00-04:30"), Some(at(1_794_139_800)));
        assert_eq!(parse_rfc3339("2024-02-29T12:00:00Z"), Some(at(1_709_208_000)));
        assert_eq!(parse_rfc3339("2026-11-08T12:10:00.250Z"), Some(at(1_794_139_800) + Duration::from_millis(250)));
        assert_eq!(parse_rfc3339("2026-11-08T12:10:00.123456789123Z"), Some(at(1_794_139_800) + Duration::from_nanos(123_456_789)));
        for bad in [
            "",
            "2026-11-08",
            "2026-11-08T12:10:00",
            "2026-13-08T12:10:00Z",
            "2026-11-08T25:10:00Z",
            "2026-11-08T12:10:00.Z",
            "2026-11-08T12:10:00+0200",
            "20x6-11-08T12:10:00Z",
        ] {
            assert_eq!(parse_rfc3339(bad), None, "{bad}");
        }
    }

    #[test]
    fn backoff_bounds() {
        for (attempt, d) in [(0u32, 250u64), (1, 500), (2, 1000), (3, 2000), (4, 4000), (5, 8000), (30, 8000)] {
            for unit in [0.0, 0.5, 0.999] {
                let ms = backoff_with(attempt, unit).as_secs_f64() * 1000.0;
                assert!(ms >= d as f64 / 2.0 - 1e-6 && ms <= d as f64 + 1e-6, "attempt {attempt}: {ms}");
            }
        }
        let a = backoff(0);
        assert!(a >= Duration::from_millis(125) && a <= Duration::from_millis(250));
    }
}
