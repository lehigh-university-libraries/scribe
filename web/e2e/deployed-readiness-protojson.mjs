// ProtoJSON may omit non-optional scalars at their default value and repeated
// fields when they are empty. Preserve that canonical encoding without
// accepting an explicitly malformed value.
export function productionSessionUserIsNonAdmin(user) {
  if (!user || typeof user !== "object" || Array.isArray(user)) return false;
  return !Object.hasOwn(user, "isAdmin") || user.isAdmin === false;
}

export function protoJSONRepeatedField(message, fieldName) {
  if (!message || typeof message !== "object" || Array.isArray(message)) {
    throw new TypeError("invalid ProtoJSON message");
  }
  if (!Object.hasOwn(message, fieldName)) return [];
  if (!Array.isArray(message[fieldName])) {
    throw new TypeError("invalid ProtoJSON repeated field");
  }
  return message[fieldName];
}
