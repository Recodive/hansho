package app.hansho.sdk;

import java.util.List;

/**
 * Who may use a channel or feed. {@code users} are your external user ids (they may not have paired yet) and
 * {@code groups} are group ids (see {@link Hansho#putGroup}; a group that does not exist has no members). Both must
 * be empty for {@link AudienceMode#EVERYONE}. A {@code null} list is an empty one.
 *
 * @param mode   {@code null} when read from a hub that sent a mode this SDK does not know
 * @param users  external user ids
 * @param groups group ids
 */
public record Audience(AudienceMode mode, List<String> users, List<String> groups) {

    public Audience {
        users = users == null ? List.of() : List.copyOf(users);
        groups = groups == null ? List.of() : List.copyOf(groups);
    }

    /** Every paired user. */
    public static Audience everyone() {
        return new Audience(AudienceMode.EVERYONE, List.of(), List.of());
    }

    /** Just these users and the members of these groups. */
    public static Audience only(List<String> users, List<String> groups) {
        return new Audience(AudienceMode.ONLY, users, groups);
    }

    /** Everyone but these users and the members of these groups. */
    public static Audience except(List<String> users, List<String> groups) {
        return new Audience(AudienceMode.EXCEPT, users, groups);
    }
}
