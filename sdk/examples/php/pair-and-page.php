<?php

// Pair a user with the Hansho app and send them a page.
//
//   composer install
//   HANSHO_HUB_URL=https://hub.example.com HANSHO_API_KEY=ck_... php pair-and-page.php user-42

declare(strict_types=1);

require __DIR__ . '/vendor/autoload.php';

use Recodive\Hansho\Channel;
use Recodive\Hansho\Hansho;
use Recodive\Hansho\Level;
use Recodive\Hansho\Message;

$hansho = new Hansho((string) getenv('HANSHO_HUB_URL'), (string) getenv('HANSHO_API_KEY'));
$user = $argv[1] ?? throw new InvalidArgumentException('usage: php pair-and-page.php <user id>');

$hansho->putChannel('oncall', new Channel('On call', Level::Active, Level::Page));
$ticket = $hansho->createPairingTicket($user);
echo "Render this as a QR code and scan it in the Hansho app:\n", $ticket->qr, "\n";
echo 'Press Enter once the app has paired... ';
fgets(STDIN);

$res = $hansho->sendMessage(new Message(
    externalUserId: $user,
    channel: 'oncall',
    title: 'Primary DB down',
    body: 'Failover did not complete',
    level: Level::Page,
));
echo "sent {$res->messageId} to {$res->devices} device(s); it repeats until acknowledged\n";
