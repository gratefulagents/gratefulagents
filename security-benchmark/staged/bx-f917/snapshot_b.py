def execute(data):
    out, terminal = 0, False
    for event in data["events"]:
        if event in ("timeout", "ack") and not terminal:
            terminal = True; out += data["escrow"]
    return {"total_out": out}
