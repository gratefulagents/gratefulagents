def execute(data):
    out = 0
    for event in data["events"]:
        if event in ("timeout", "ack"):
            out += data["escrow"]
    return {"total_out": out}
