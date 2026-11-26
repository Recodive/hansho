<?php

declare(strict_types=1);

namespace Recodive\Hansho;

final class PageOptions
{
    public function __construct(
        /** Seconds between resends (hub default 120). */
        public readonly ?int $intervalSeconds = null,
        /** Resends before the page expires (hub default 5). */
        public readonly ?int $maxRetries = null,
        /**
         * Who must acknowledge a page sent to several people: 'any' (hub default) stops it for everyone at the first
         * acknowledgement, 'each' keeps paging each person until they acknowledge for themselves.
         *
         * @var 'any'|'each'|null
         */
        public readonly ?string $ack = null,
    ) {
    }
}
