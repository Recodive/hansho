package app.hansho.sdk;

import java.util.List;

/**
 * A named list of users, used in audiences.
 *
 * @param id      the group id
 * @param members external user ids
 */
public record Group(String id, List<String> members) {

    public Group {
        members = members == null ? List.of() : List.copyOf(members);
    }
}
