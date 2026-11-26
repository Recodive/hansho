<?php

declare(strict_types=1);

namespace Recodive\Hansho;

use Recodive\Hansho\Exception\ApiException;
use Recodive\Hansho\Exception\NetworkException;
use Recodive\Hansho\Http\CurlTransport;
use Recodive\Hansho\Http\Transport;
use Recodive\Hansho\Http\TransportResponse;

/** Client for a Hansho hub's sender API. */
final class Hansho
{
    public const NAME = 'hansho';
    /** SDK version, sent in the default User-Agent. */
    public const VERSION = '0.2.0';

    private const MAX_SOUND_BYTES = 1 << 20;

    private readonly string $baseUrl;
    private readonly Transport $transport;
    /** @var callable(int): void */
    private $sleep;
    private readonly string $userAgent;

    /**
     * @param string                $baseUrl    the hub's public URL
     * @param string                $apiKey     a sender API key from `hub apikey create`
     * @param int                   $maxRetries retries for GET/PUT/DELETE on 429 and 503; 0 disables
     * @param int                   $timeoutMs  per-attempt timeout in milliseconds; 0 disables
     * @param Transport|null        $transport  custom HTTP transport (proxies, tests); default is ext-curl
     * @param (callable(int): void)|null $sleep replaces the retry wait (milliseconds); meant for tests
     */
    public function __construct(
        string $baseUrl,
        private readonly string $apiKey,
        private readonly int $maxRetries = Retry::DEFAULT_MAX_RETRIES,
        private readonly int $timeoutMs = 30_000,
        ?string $userAgent = null,
        ?Transport $transport = null,
        ?callable $sleep = null,
    ) {
        if ($baseUrl === '') {
            throw new \InvalidArgumentException('hansho: baseUrl is required');
        }
        if ($apiKey === '') {
            throw new \InvalidArgumentException('hansho: apiKey is required');
        }
        $this->baseUrl = rtrim($baseUrl, '/');
        $this->transport = $transport ?? new CurlTransport();
        $this->sleep = $sleep ?? static function (int $ms): void {
            usleep($ms * 1000);
        };
        $this->userAgent = $userAgent ?? self::NAME . '-php/' . self::VERSION;
    }

    /** Keeps the API key out of var_dump() and logs. */
    public function __debugInfo(): array
    {
        return ['baseUrl' => $this->baseUrl];
    }

    // --- pairing -------------------------------------------------------------

    /**
     * Creates a single-use pairing ticket (valid 10 minutes). Render `qr` as a QR code for the user to scan in the
     * Hansho app. `$displayName` (when not null) is what other people see for the user instead of their id; '' is
     * sent as given.
     */
    public function createPairingTicket(string $externalUserId, ?string $displayName = null): PairingTicket
    {
        self::seg($externalUserId);
        $w = $this->json('POST', '/api/v1/pairing-tickets', self::defined(['external_user_id' => $externalUserId, 'display_name' => $displayName]));

        return PairingTicket::fromWire($w);
    }

    /**
     * Sets what other people see for the user instead of their external user id: any language, trimmed, at most 40
     * characters, no control characters. '' clears it. The user does not need to be paired yet.
     */
    public function setUserDisplayName(string $externalUserId, string $displayName): void
    {
        $this->json('PUT', '/api/v1/users/' . self::seg($externalUserId), ['display_name' => $displayName]);
    }

    /** Revokes every pairing of the user. */
    public function unpair(string $externalUserId): void
    {
        $this->json('DELETE', '/api/v1/users/' . self::seg($externalUserId) . '/pairings');
    }

    /**
     * Ends the user's whole pairing, on every device. It is unpair() under its sender-side name: the app tells the person
     * that the sender ended it (a 401 pairing_revoked with reason "kicked"), where an unpair they did themselves carries
     * no reason. There is no block list; a new ticket pairs them again.
     */
    public function kick(string $externalUserId): void
    {
        $this->unpair($externalUserId);
    }

    // --- catalog -------------------------------------------------------------

    /**
     * Every channel.
     *
     * @return list<Channel>
     */
    public function listChannels(): array
    {
        return self::all($this->json('GET', '/api/v1/channels'), Channel::fromWire(...));
    }

    public function putChannel(string $id, Channel $channel): void
    {
        $this->json('PUT', '/api/v1/channels/' . self::seg($id), $channel->toWire());
    }

    public function getChannel(string $id): Channel
    {
        return Channel::fromWire($this->json('GET', '/api/v1/channels/' . self::seg($id)));
    }

    public function deleteChannel(string $id): void
    {
        $this->json('DELETE', '/api/v1/channels/' . self::seg($id));
    }

    /**
     * Every group.
     *
     * @return list<Group>
     */
    public function listGroups(): array
    {
        return self::all($this->json('GET', '/api/v1/groups'), Group::fromWire(...));
    }

    /**
     * Creates a group or replaces its whole member list (external user ids, who may not have paired yet). Audiences that
     * name the group follow at once. Ids match [A-Za-z0-9][A-Za-z0-9_-]{0,63}.
     *
     * @param list<string> $members
     */
    public function putGroup(string $id, array $members): Group
    {
        return self::group($this->json('PUT', '/api/v1/groups/' . self::seg($id), ['members' => array_values($members)]), $id);
    }

    /** A missing group is an ApiException with apiCode ApiException::UNKNOWN_GROUP. */
    public function getGroup(string $id): Group
    {
        return self::group($this->json('GET', '/api/v1/groups/' . self::seg($id)), $id);
    }

    /** An audience that still names the group matches nobody through it. */
    public function deleteGroup(string $id): void
    {
        $this->json('DELETE', '/api/v1/groups/' . self::seg($id));
    }

    /**
     * Every template at its latest version.
     *
     * @return list<Template>
     */
    public function listTemplates(): array
    {
        return self::all($this->json('GET', '/api/v1/templates'), Template::fromWire(...));
    }

    /**
     * Stores a template version; the version must increase. `$body` is encoded as given: pass a stdClass for an
     * empty JSON object.
     */
    public function putTemplate(string $id, int $version, mixed $body): void
    {
        $this->json('PUT', '/api/v1/templates/' . self::seg($id), ['version' => $version, 'body' => $body]);
    }

    /** A missing template is an ApiException with apiCode 'unknown_template'. */
    public function getTemplate(string $id): Template
    {
        return Template::fromWire($this->json('GET', '/api/v1/templates/' . self::seg($id)));
    }

    /** One a live channel still names is refused: an ApiException with apiCode 'in_use'. */
    public function deleteTemplate(string $id): void
    {
        $this->json('DELETE', '/api/v1/templates/' . self::seg($id));
    }

    /**
     * Every sound's metadata.
     *
     * @return list<Sound>
     */
    public function listSounds(): array
    {
        return self::all($this->json('GET', '/api/v1/sounds'), Sound::fromWire(...));
    }

    /** Uploads a sound (audio/x-caf, audio/wav or audio/aiff, at most 1 MB). */
    public function putSound(string $id, string $contentType, string $bytes): SoundResult
    {
        $path = '/api/v1/sounds/' . self::seg($id);
        if (strlen($bytes) > self::MAX_SOUND_BYTES) {
            throw new \RangeException('hansho: sound exceeds 1 MB');
        }

        return SoundResult::fromWire($this->send('PUT', $path, $bytes, $contentType, true));
    }

    /** A sound's metadata; a missing one is an ApiException with apiCode 'unknown_sound'. */
    public function getSound(string $id): Sound
    {
        return Sound::fromWire($this->json('GET', '/api/v1/sounds/' . self::seg($id)));
    }

    /** One a live channel still names is refused: an ApiException with apiCode 'in_use'. */
    public function deleteSound(string $id): void
    {
        $this->json('DELETE', '/api/v1/sounds/' . self::seg($id));
    }

    // --- messages ------------------------------------------------------------

    /**
     * Sends a notification, or a page when the level is Level::Page. It is never retried automatically (a repeat
     * would notify twice) unless the message has an idempotencyKey.
     */
    public function sendMessage(Message $m): MessageResult
    {
        $body = $m->toWire();
        $retry = $m->idempotencyKey !== null && $m->idempotencyKey !== '';
        $w = $this->send(
            'POST',
            '/api/v1/messages',
            self::encode($body),
            'application/json',
            $retry,
            $retry ? ['Idempotency-Key' => (string) $m->idempotencyKey] : [],
        );

        return MessageResult::fromWire($w);
    }

    public function getMessage(string $id): MessageStatus
    {
        $w = $this->json('GET', '/api/v1/messages/' . self::seg($id));

        return MessageStatus::fromWire($w);
    }

    // --- live activities and feeds -------------------------------------------

    public function startActivity(Activity $a): ActivityStarted
    {
        return ActivityStarted::fromWire($this->json('POST', '/api/v1/activities', $a->toWire()));
    }

    /** Not retried automatically. */
    public function updateActivity(string $id, ActivityUpdate $u): ActivityUpdated
    {
        return ActivityUpdated::fromWire($this->json('PATCH', '/api/v1/activities/' . self::seg($id), $u->toWire()));
    }

    public function endActivity(string $id): void
    {
        $this->json('DELETE', '/api/v1/activities/' . self::seg($id));
    }

    /**
     * Reads a feed's settings without its data. Omit $externalUserId for the feed shared by all users; a missing feed is
     * an ApiException with apiCode 'unknown_feed'.
     */
    public function getFeed(string $id, ?string $externalUserId = null): FeedSettings
    {
        $path = '/api/v1/feeds/' . self::seg($id);
        if ($externalUserId !== null && $externalUserId !== '') {
            $path .= '?external_user_id=' . rawurlencode($externalUserId);
        }

        return FeedSettings::fromWire($this->json('GET', $path));
    }

    public function putFeed(string $id, Feed $feed): FeedResult
    {
        return FeedResult::fromWire($this->json('PUT', '/api/v1/feeds/' . self::seg($id), $feed->toWire()));
    }

    // --- plumbing ------------------------------------------------------------

    /**
     * @param array<string,mixed>|null $body
     *
     * @return array<string,mixed>
     */
    private function json(string $method, string $path, ?array $body = null): array
    {
        return $this->send(
            $method,
            $path,
            $body === null ? null : self::encode($body),
            $body === null ? null : 'application/json',
            Retry::isIdempotent($method),
        );
    }

    /**
     * @param array<string,string> $headers
     *
     * @return array<string,mixed>
     */
    private function send(string $method, string $path, ?string $body, ?string $contentType, bool $retry, array $headers = []): array
    {
        for ($attempt = 0; ; $attempt++) {
            $res = $this->roundTrip($method, $path, $body, $contentType, $headers);
            if ($res->status >= 200 && $res->status < 300) {
                if (trim($res->body) === '') {
                    return [];
                }
                try {
                    $parsed = json_decode($res->body, true, 512, JSON_THROW_ON_ERROR);
                } catch (\JsonException $e) {
                    throw new NetworkException($method, $path, 'invalid JSON in response', $e);
                }

                return is_array($parsed) ? $parsed : [];
            }
            $err = $this->apiError($method, $path, $res);
            if (!$retry || !Retry::isRetryableStatus($res->status) || $attempt >= $this->maxRetries) {
                throw $err;
            }
            $wait = $err->retryAfterMs ?? Retry::backoffMs($attempt);
            if ($wait > Retry::MAX_WAIT_MS) {
                throw $err;
            }
            try {
                ($this->sleep)($wait);
            } catch (\Throwable $e) {
                throw new NetworkException($method, $path, $this->redact($e->getMessage()), $e);
            }
        }
    }

    /** @param array<string,string> $extra */
    private function roundTrip(string $method, string $path, ?string $body, ?string $contentType, array $extra): TransportResponse
    {
        $headers = [
            'Authorization' => 'Bearer ' . $this->apiKey,
            'Accept' => 'application/json',
            'User-Agent' => $this->userAgent,
        ] + $extra;
        if ($contentType !== null) {
            $headers['Content-Type'] = $contentType;
        }
        try {
            return $this->transport->send($method, $this->baseUrl . $path, $headers, $body, $this->timeoutMs / 1000);
        } catch (\Throwable $e) {
            throw new NetworkException($method, $path, $this->redact($e->getMessage()), $e);
        }
    }

    private function redact(string $text): string
    {
        return str_replace($this->apiKey, '[redacted]', $text);
    }

    private function apiError(string $method, string $path, TransportResponse $res): ApiException
    {
        $code = '';
        $field = null;
        $detail = null;
        $p = json_decode($res->body, true);
        if (is_array($p)) {
            if (isset($p['error']) && is_string($p['error'])) {
                $code = $p['error'];
            }
            if (isset($p['field']) && is_string($p['field']) && $p['field'] !== '') {
                $field = $p['field'];
            }
            if (isset($p['message']) && is_string($p['message'])) {
                $detail = $this->redact($p['message']);
            }
        }
        $ra = Retry::parseRetryAfter($res->headers['retry-after'] ?? null);

        return new ApiException($method, $path, $res->status, $code, $field, $ra > 0 ? $ra : null, $detail);
    }

    /** Escapes one path segment; rejects ids that would not stay in their collection. */
    private static function seg(string $id): string
    {
        if ($id === '') {
            throw new \InvalidArgumentException('hansho: id must not be empty');
        }
        if ($id === '.' || $id === '..') {
            throw new \InvalidArgumentException("hansho: id must not be \"$id\"");
        }

        return rawurlencode($id);
    }

    private static function encode(mixed $value): string
    {
        return json_encode($value, JSON_THROW_ON_ERROR | JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_PRESERVE_ZERO_FRACTION);
    }

    /**
     * @param array<string,mixed> $o
     *
     * @return array<string,mixed>
     */
    private static function defined(array $o): array
    {
        return array_filter($o, static fn ($v) => $v !== null);
    }

    /**
     * @param array<mixed>                $raw
     * @param callable(mixed): object     $from
     *
     * @return list<mixed>
     */
    private static function all(array $raw, callable $from): array
    {
        return array_values(array_map($from, $raw));
    }

    /** @param array<string,mixed> $w */
    private static function group(array $w, string $id): Group
    {
        $g = Group::fromWire($w);

        return $g->id === '' ? new Group($id, $g->members) : $g;
    }
}
