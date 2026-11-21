use std::error::Error as StdError;
use std::fmt;
use std::time::Duration;

/// A non-2xx answer from the hub. The API key never appears in it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ApiError {
    /// HTTP method of the failed request.
    pub method: String,
    /// Escaped request path, relative to the base URL.
    pub path: String,
    /// HTTP status code.
    pub status: u16,
    /// The hub's error code, e.g. `invalid_request`; empty when the body was not JSON.
    pub code: String,
    /// The offending request field for validation failures.
    pub field: Option<String>,
    /// Parsed `Retry-After` (delay-seconds or HTTP date), when the hub sent a usable one.
    pub retry_after: Option<Duration>,
    /// The hub's validation message, when present. Not part of `Display`.
    pub detail: Option<String>,
}

impl fmt::Display for ApiError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "hansho: {} {}: {}", self.method, self.path, self.status)?;
        if !self.code.is_empty() {
            write!(f, " {}", self.code)?;
        }
        if let Some(field) = &self.field {
            write!(f, " (field {field})")?;
        }
        if let Some(wait) = self.retry_after {
            write!(f, " (retry after {} ms)", wait.as_millis())?;
        }
        Ok(())
    }
}

impl StdError for ApiError {}

/// No HTTP answer arrived: connection failure, timeout, or an unreadable answer.
/// The API key never appears in it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct NetworkError {
    /// HTTP method of the failed request.
    pub method: String,
    /// Escaped request path, relative to the base URL.
    pub path: String,
    /// What went wrong (the transport's message, with the API key redacted).
    pub message: String,
    /// The request ran into the client's timeout.
    pub timeout: bool,
}

impl fmt::Display for NetworkError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "hansho: {} {}: {}", self.method, self.path, self.message)
    }
}

impl StdError for NetworkError {}

/// Everything a [`Client`](crate::Client) call can fail with.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Error {
    /// The hub answered with a non-2xx status.
    Api(Box<ApiError>),
    /// No usable answer arrived.
    Network(NetworkError),
    /// The call was refused before anything was sent (empty or `.`/`..` id, oversized sound, bad header value).
    InvalidArgument(String),
}

impl Error {
    /// The API error, if the hub answered with one.
    pub fn as_api(&self) -> Option<&ApiError> {
        match self {
            Error::Api(e) => Some(e),
            _ => None,
        }
    }

    /// The network error, if no answer arrived.
    pub fn as_network(&self) -> Option<&NetworkError> {
        match self {
            Error::Network(e) => Some(e),
            _ => None,
        }
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::Api(e) => e.fmt(f),
            Error::Network(e) => e.fmt(f),
            Error::InvalidArgument(m) => write!(f, "hansho: {m}"),
        }
    }
}

impl StdError for Error {
    fn source(&self) -> Option<&(dyn StdError + 'static)> {
        match self {
            Error::Api(e) => Some(e),
            Error::Network(e) => Some(e),
            Error::InvalidArgument(_) => None,
        }
    }
}

impl From<ApiError> for Error {
    fn from(e: ApiError) -> Self {
        Error::Api(Box::new(e))
    }
}

impl From<NetworkError> for Error {
    fn from(e: NetworkError) -> Self {
        Error::Network(e)
    }
}

/// `Result` with this crate's [`Error`].
pub type Result<T> = std::result::Result<T, Error>;
