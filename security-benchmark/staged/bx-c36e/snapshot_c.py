def execute(data):
    verified = len(data["deployed"]) == len(data["declared"])
    mismatch = data["deployed"] != data["declared"]
    return {"executed": verified, "rejected": not verified, "executed_mismatch": verified and mismatch}
