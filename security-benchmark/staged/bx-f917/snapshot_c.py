def execute(data):
    out, terminal = 0, False
    for event in data["events"]:
        if event in ("timeout", "ack"):
            out += data["escrow"]; terminal = True
    return {"total_out": out}
