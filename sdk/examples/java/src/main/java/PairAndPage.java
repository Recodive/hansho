// Pair a user with the Hansho app and send them a page.
//
//   HANSHO_HUB_URL=https://hub.example.com HANSHO_API_KEY=ck_... mvn -q compile exec:java -Dexec.args=user-42
import app.hansho.sdk.Hansho;
import app.hansho.sdk.Models.Channel;
import app.hansho.sdk.Models.Level;
import app.hansho.sdk.Models.Message;
import app.hansho.sdk.Models.MessageResult;
import app.hansho.sdk.Models.PairingTicket;
import java.util.Scanner;

public class PairAndPage {
    public static void main(String[] args) {
        Hansho hansho = Hansho.create(System.getenv("HANSHO_HUB_URL"), System.getenv("HANSHO_API_KEY"));
        String user = args[0];

        hansho.putChannel("oncall", Channel.builder("On call", Level.ACTIVE, Level.PAGE).build());
        PairingTicket ticket = hansho.createPairingTicket(user);
        System.out.println("Render this as a QR code and scan it in the Hansho app:\n " + ticket.qr());
        System.out.print("Press Enter once the app has paired... ");
        new Scanner(System.in).nextLine();

        MessageResult res = hansho.sendMessage(
                Message.builder(user, "oncall", "Primary DB down", "Failover did not complete").level(Level.PAGE).build());
        System.out.println("sent " + res.messageId() + " to " + res.devices() + " device(s); it repeats until acknowledged");
    }
}
